// Copyright 2017 The kubecfg authors
//
//
//    Licensed under the Apache License, Version 2.0 (the "License");
//    you may not use this file except in compliance with the License.
//    You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
//    Unless required by applicable law or agreed to in writing, software
//    distributed under the License is distributed on an "AS IS" BASIS,
//    WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//    See the License for the specific language governing permissions and
//    limitations under the License.

package utils

import (
	"fmt"
	"strings"
	"sync"

	"github.com/emicklei/go-restful-swagger12"
	"github.com/googleapis/gnostic/OpenAPIv2"
	log "github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

type memcachedDiscoveryClient struct {
	cl   discovery.DiscoveryInterface
	lock sync.RWMutex

	// Remember to update Invalidate() below when adding fields
	servergroups    *metav1.APIGroupList
	serverresources map[string]*metav1.APIResourceList
	schemas         map[string]*swagger.ApiDeclaration
	schema          *openapi_v2.Document
	serverVersion   *version.Info
}

// NewMemcachedDiscoveryClient creates a new DiscoveryClient that
// caches results in memory
func NewMemcachedDiscoveryClient(cl discovery.DiscoveryInterface) discovery.CachedDiscoveryInterface {
	c := &memcachedDiscoveryClient{cl: cl}
	c.Invalidate()
	return c
}

func (c *memcachedDiscoveryClient) Fresh() bool {
	return true
}

func (c *memcachedDiscoveryClient) Invalidate() {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.servergroups = nil
	c.serverresources = make(map[string]*metav1.APIResourceList)
	c.schemas = make(map[string]*swagger.ApiDeclaration)
	c.schema = nil
	c.serverVersion = nil
}

func (c *memcachedDiscoveryClient) RESTClient() rest.Interface {
	return c.cl.RESTClient()
}

func (c *memcachedDiscoveryClient) ServerGroups() (*metav1.APIGroupList, error) {
	c.lock.Lock()
	defer c.lock.Unlock()

	var err error
	if c.servergroups != nil {
		return c.servergroups, nil
	}
	c.servergroups, err = c.cl.ServerGroups()
	return c.servergroups, err
}

func (c *memcachedDiscoveryClient) ServerResourcesForGroupVersion(groupVersion string) (*metav1.APIResourceList, error) {
	c.lock.Lock()
	defer c.lock.Unlock()

	var err error
	if v := c.serverresources[groupVersion]; v != nil {
		return v, nil
	}
	c.serverresources[groupVersion], err = c.cl.ServerResourcesForGroupVersion(groupVersion)
	return c.serverresources[groupVersion], err
}

func (c *memcachedDiscoveryClient) ServerResources() ([]*metav1.APIResourceList, error) {
	apiGroups, err := c.ServerGroups()
	if err != nil {
		return nil, err
	}

	result := []*metav1.APIResourceList{}

	for _, apiGroup := range apiGroups.Groups {
		for _, version := range apiGroup.Versions {
			resources, err := c.ServerResourcesForGroupVersion(version.GroupVersion)
			if err != nil {
				return nil, err
			}

			result = append(result, resources)
		}
	}

	return result, nil
}

func (c *memcachedDiscoveryClient) ServerPreferredResources() ([]*metav1.APIResourceList, error) {
	serverGroupList, err := c.ServerGroups()
	if err != nil {
		return nil, err
	}

	result := []*metav1.APIResourceList{}

	grVersions := map[schema.GroupResource]string{}                         // selected version of a GroupResource
	grApiResources := map[schema.GroupResource]*metav1.APIResource{}        // selected APIResource for a GroupResource
	gvApiResourceLists := map[schema.GroupVersion]*metav1.APIResourceList{} // blueprint for a APIResourceList for later grouping

	for _, apiGroup := range serverGroupList.Groups {
		for _, version := range apiGroup.Versions {
			groupVersion := schema.GroupVersion{Group: apiGroup.Name, Version: version.Version}
			apiResourceList, err := c.ServerResourcesForGroupVersion(version.GroupVersion)
			if err != nil {
				return nil, err
			}

			// create empty list which is filled later in another loop
			emptyApiResourceList := metav1.APIResourceList{
				GroupVersion: version.GroupVersion,
			}
			gvApiResourceLists[groupVersion] = &emptyApiResourceList
			result = append(result, &emptyApiResourceList)

			for i := range apiResourceList.APIResources {
				apiResource := &apiResourceList.APIResources[i]
				if strings.Contains(apiResource.Name, "/") {
					continue
				}
				gv := schema.GroupResource{Group: apiGroup.Name, Resource: apiResource.Name}
				if _, ok := grApiResources[gv]; ok && version.Version != apiGroup.PreferredVersion.Version {
					// only override with preferred version
					continue
				}
				grVersions[gv] = version.Version
				grApiResources[gv] = apiResource
			}
		}
	}

	// group selected APIResources according to GroupVersion into APIResourceLists
	for groupResource, apiResource := range grApiResources {
		version := grVersions[groupResource]
		groupVersion := schema.GroupVersion{Group: groupResource.Group, Version: version}
		apiResourceList := gvApiResourceLists[groupVersion]
		apiResourceList.APIResources = append(apiResourceList.APIResources, *apiResource)
	}

	return result, nil
}

func (c *memcachedDiscoveryClient) ServerPreferredNamespacedResources() ([]*metav1.APIResourceList, error) {
	all, err := c.ServerPreferredResources()
	return discovery.FilteredBy(discovery.ResourcePredicateFunc(
		func(gv string, r *metav1.APIResource) bool {
			return r.Namespaced
		}), all), err
}

func (c *memcachedDiscoveryClient) ServerVersion() (*version.Info, error) {
	c.lock.Lock()
	defer c.lock.Unlock()

	if c.serverVersion == nil {
		info, err := c.cl.ServerVersion()
		if err != nil {
			return nil, err
		}
		c.serverVersion = info
	}

	return c.serverVersion, nil
}

func (c *memcachedDiscoveryClient) SwaggerSchema(version schema.GroupVersion) (*swagger.ApiDeclaration, error) {
	key := version.String()

	c.lock.Lock()
	defer c.lock.Unlock()

	if c.schemas[key] != nil {
		return c.schemas[key], nil
	}

	schema, err := c.cl.SwaggerSchema(version)
	if err != nil {
		return nil, err
	}

	c.schemas[key] = schema
	return schema, nil
}

func (c *memcachedDiscoveryClient) OpenAPISchema() (*openapi_v2.Document, error) {
	c.lock.Lock()
	defer c.lock.Unlock()

	if c.schema != nil {
		return c.schema, nil
	}

	schema, err := c.cl.OpenAPISchema()
	if err != nil {
		return nil, err
	}

	c.schema = schema
	return schema, nil
}

var _ discovery.CachedDiscoveryInterface = &memcachedDiscoveryClient{}

// ClientForResource returns the ResourceClient for a given object
func ClientForResource(pool dynamic.ClientPool, disco discovery.DiscoveryInterface, obj runtime.Object, defNs string) (dynamic.ResourceInterface, error) {
	gvk := obj.GetObjectKind().GroupVersionKind()

	client, err := pool.ClientForGroupVersionKind(gvk)
	if err != nil {
		return nil, err
	}

	resource, err := serverResourceForGroupVersionKind(disco, gvk)
	if err != nil {
		return nil, err
	}

	meta, err := meta.Accessor(obj)
	if err != nil {
		return nil, err
	}
	namespace := meta.GetNamespace()
	if namespace == "" {
		namespace = defNs
	}

	log.Debugf("Fetching client for %s namespace=%s", resource, namespace)
	rc := client.Resource(resource, namespace)
	return rc, nil
}

func serverResourceForGroupVersionKind(disco discovery.ServerResourcesInterface, gvk schema.GroupVersionKind) (*metav1.APIResource, error) {
	resources, err := disco.ServerResourcesForGroupVersion(gvk.GroupVersion().String())
	if err != nil {
		return nil, fmt.Errorf("unable to fetch resource description for %s: %v", gvk.GroupVersion(), err)
	}

	for _, r := range resources.APIResources {
		if r.Kind == gvk.Kind {
			log.Debugf("Using resource '%s' for %s", r.Name, gvk)
			return &r, nil
		}
	}

	return nil, fmt.Errorf("Server is unable to handle %s", gvk)
}
