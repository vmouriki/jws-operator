package controllers

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	webserversv1alpha1 "github.com/web-servers/jws-operator/api/v1alpha1"
	webserverstests "github.com/web-servers/jws-operator/test/framework"
)

var _ = Describe("WebServer controller", func() {
	/* AfterEach(func() {
		ctx := context.Background()
		webserver := &webserversv1alpha1.WebServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-webserver",
				Namespace: "default",
			},
		}
		Expect(k8sClient.Delete(ctx, webserver)).Should(Succeed())

	}) */
	Context("First Test", func() {
		It("Other Basic test", func() {
			By("By creating a new WebServer")
			fmt.Printf("By creating a new WebServer\n")
			if !noskip {
				fmt.Printf("other_basic_testy skipped\n")
				return
			}
			ctx := context.Background()
			name := "other-basic-test"
			namespace := "default"
			webserver := &webserversv1alpha1.WebServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: namespace,
				},
				Spec: webserversv1alpha1.WebServerSpec{
					ApplicationName: "test-tomcat-demo",
					Replicas:        int32(2),
					WebImage: &webserversv1alpha1.WebImageSpec{
						// ApplicationImage: "quay.io/jfclere/tomcat-demo",
						ApplicationImage: "registry.redhat.io/jboss-webserver-5/webserver54-openjdk8-tomcat9-openshift-rhel8",
						ImagePullSecret:  "jfc",
					},
				},
			}

			// make sure we cleanup
			defer func() {
				k8sClient.Delete(context.Background(), webserver)
				time.Sleep(time.Second * 5)
			}()

			// create the webserver
			Expect(k8sClient.Create(ctx, webserver)).Should(Succeed())

			// Check it is started.
			webserverLookupKey := types.NamespacedName{Name: name, Namespace: namespace}
			createdWebserver := &webserversv1alpha1.WebServer{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, webserverLookupKey, createdWebserver)
				if err != nil {
					return false
				}
				return true
			}, time.Second*20, time.Millisecond*250).Should(BeTrue())
			fmt.Printf("new WebServer Name: %s Namespace: %s\n", createdWebserver.ObjectMeta.Name, createdWebserver.ObjectMeta.Namespace)

			// are the corresponding pods ready?
			Eventually(func() bool {
				err := webserverstests.WaitUntilReady(k8sClient, ctx, thetest, createdWebserver)
				if err != nil {
					return false
				}
				return true
			}, timeout, retryInterval).Should(BeTrue())

		})
	})
})
