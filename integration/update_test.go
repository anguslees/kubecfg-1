// +build integration

package integration

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/pkg/api"
	"k8s.io/client-go/pkg/api/v1"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("update", func() {
	var c corev1.CoreV1Interface
	var ns string
	const testName = "testobj"

	BeforeEach(func() {
		c = corev1.NewForConfigOrDie(clusterConfigOrDie())
		ns = createNsOrDie(c, "update")
	})
	AfterEach(func() {
		deleteNsOrDie(c, ns)
	})

	Describe("A simple update", func() {
		var cm *v1.ConfigMap
		BeforeEach(func() {
			cm = &v1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: testName},
				Data:       map[string]string{"foo": "bar"},
			}
		})

		JustBeforeEach(func() {
			err := runKubecfgWith([]string{"update", "-vv", "-n", ns}, []runtime.Object{cm})
			Expect(err).NotTo(HaveOccurred())
		})

		Context("With no existing state", func() {
			It("should produce expected object", func() {
				Expect(c.ConfigMaps(ns).Get(testName, metav1.GetOptions{})).
					To(WithTransform(cmData, HaveKeyWithValue("foo", "bar")))
			})
		})

		Context("With existing object", func() {
			BeforeEach(func() {
				_, err := c.ConfigMaps(ns).Create(cm)
				Expect(err).To(Not(HaveOccurred()))
			})

			It("should succeed", func() {

				Expect(c.ConfigMaps(ns).Get(testName, metav1.GetOptions{})).
					To(WithTransform(cmData, HaveKeyWithValue("foo", "bar")))
			})
		})

		Context("With modified object", func() {
			BeforeEach(func() {
				otherCm := &v1.ConfigMap{
					ObjectMeta: cm.ObjectMeta,
					Data:       map[string]string{"foo": "not bar"},
				}

				_, err := c.ConfigMaps(ns).Create(otherCm)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should update the object", func() {
				Expect(c.ConfigMaps(ns).Get(testName, metav1.GetOptions{})).
					To(WithTransform(cmData, HaveKeyWithValue("foo", "bar")))
			})
		})
	})

	Describe("An update with mixed namespaces", func() {
		var ns2 string
		BeforeEach(func() {
			ns2 = createNsOrDie(c, "update")
		})
		AfterEach(func() {
			deleteNsOrDie(c, ns2)
		})

		var objs []runtime.Object
		BeforeEach(func() {
			objs = []runtime.Object{
				&v1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{Name: "nons"},
				},
				&v1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "ns1"},
				},
				&v1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{Namespace: ns2, Name: "ns2"},
				},
			}
		})

		JustBeforeEach(func() {
			err := runKubecfgWith([]string{"update", "-vv", "-n", ns}, objs)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should create objects in the correct namespaces", func() {
			Expect(c.ConfigMaps(ns).Get("nons", metav1.GetOptions{})).
				NotTo(BeNil())

			Expect(c.ConfigMaps(ns).Get("ns1", metav1.GetOptions{})).
				NotTo(BeNil())

			Expect(c.ConfigMaps(ns2).Get("ns2", metav1.GetOptions{})).
				NotTo(BeNil())
		})
	})

	Describe("Complex modification", func() {
		var old, new *v1.Service
		var kubecfgInput []runtime.Object
		var err error

		BeforeEach(func() {
			old = &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name: testName,
				},
				Spec: v1.ServiceSpec{
					Ports: []v1.ServicePort{{
						Port: 80,
					}},
				},
			}

			tmp, err := api.Scheme.Copy(old) // Deep copy
			Expect(err).NotTo(HaveOccurred())
			new = tmp.(*v1.Service)
			kubecfgInput = []runtime.Object{new}
		})

		JustBeforeEach(func() {
			_, err := c.Services(ns).Create(old)
			Expect(err).NotTo(HaveOccurred())

			err = runKubecfgWith([]string{"update", "-vv", "-n", ns}, kubecfgInput)
			Expect(err).NotTo(HaveOccurred())

		})

		Context("with a new set of values", func() {
			var actual *v1.Service

			BeforeEach(func() {
				old.ObjectMeta.Annotations = map[string]string{"a": "b"}
				new.ObjectMeta.Annotations = old.ObjectMeta.Annotations

				old.ObjectMeta.Labels = map[string]string{"foo": "bar"}
				new.ObjectMeta.Labels = map[string]string{"baz": "xyzzy"}

				// NB: ServiceSpec.Ports was chosen to exercise
				// patchStrategy=merge (patchMergeKey="port")
				new.Spec.Ports = []v1.ServicePort{{
					Port: 81,
				}}
			})

			JustBeforeEach(func() {
				actual, err = c.Services(ns).Get(testName, metav1.GetOptions{})
				Expect(err).NotTo(HaveOccurred())
			})

			It("should not modify unchanged values", func() {
				Expect(actual.ObjectMeta.Annotations).
					To(Equal(map[string]string{"a": "b"}))
			})

			It("should update maps to exactly given values", func() {
				Expect(actual.ObjectMeta.Labels).
					To(Equal(map[string]string{"baz": "xyzzy"}))
			})

			It("should set merged arrays to exactly given values", func() {
				Expect(actual.Spec.Ports).
					To(HaveLen(1))
				Expect(actual.Spec.Ports[0].Port).
					To(Equal(int32(81)))
			})
		})

		Context("with a change to immutable field", func() {
			var actual *v1.Service

			BeforeEach(func() {
				// FIXME: this assumes minikube
				// default clusterIP ranges.  Need to
				// actually create service, then base
				// change on returned value.
				old.Spec.ClusterIP = "10.0.0.99"
				new.Spec.ClusterIP = "10.0.0.100"
			})

			JustBeforeEach(func() {
				actual, err = c.Services(ns).Get(testName, metav1.GetOptions{})
				Expect(err).NotTo(HaveOccurred())
			})

			// FIXME: unclear - we either should abort
			// with an error, xor (ideally!) somehow make
			// the update happen.  ie: indicating success
			// and *not* updating the field is
			// unacceptable.
			It("should update clusterIP", func() {
				Expect(actual.Spec.ClusterIP).
					To(Equal("10.0.0.100"))
			})
		})

		Context("with a null", func() {
			var actual *v1.Service

			BeforeEach(func() {
				old.Spec.Type = v1.ServiceTypeNodePort

				// `null` can't be encoded via
				// golang serialisation - need to
				// generate raw json object for
				// kubecfg.

				// Aka new.Spec.Type = null
				obj := &unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "v1",
						"kind":       "Service",
						"metadata": map[string]interface{}{
							"name": testName,
						},
						"spec": map[string]interface{}{
							"ports": []map[string]interface{}{{
								"port": 80,
							}},
							"type": nil,
						},
					},
				}
				kubecfgInput = []runtime.Object{obj}
			})

			JustBeforeEach(func() {
				actual, err = c.Services(ns).Get(testName, metav1.GetOptions{})
				Expect(err).NotTo(HaveOccurred())
			})

			It("should explicitly revert to default", func() {
				Expect(actual.Spec.Type).
					To(Equal(v1.ServiceTypeClusterIP))
			})
		})

		FContext("with a new resource type", func() {
			BeforeEach(func() {
				obj := &v1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{Name: testName},
					Data:       map[string]string{"foo": "bar"},
				}
				kubecfgInput = []runtime.Object{obj}
			})

			It("should create a new object", func() {
				Expect(c.ConfigMaps(ns).Get(testName, metav1.GetOptions{})).
					To(WithTransform(cmData, HaveKeyWithValue("foo", "bar")))
			})

			It("should remove old object", func() {
				Eventually(func() error {
					_, err := c.Services(ns).Get(testName, metav1.GetOptions{})
					return err
				}).Should(MatchError(ContainSubstring("not found")))
			})
		})
	})
})
