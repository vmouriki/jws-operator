package controllers

import (
	"context"
	"fmt"
	"sort"
	"time"

	webserversv1alpha1 "github.com/web-servers/jws-operator/api/v1alpha1"

	appsv1 "github.com/openshift/api/apps/v1"
	buildv1 "github.com/openshift/api/build/v1"
	imagev1 "github.com/openshift/api/image/v1"
	routev1 "github.com/openshift/api/route/v1"
	kbappsv1 "k8s.io/api/apps/v1"

	corev1 "k8s.io/api/core/v1"
	rbac "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func isOpenShift(c *rest.Config) bool {
	var err error
	var dcclient *discovery.DiscoveryClient
	dcclient, err = discovery.NewDiscoveryClientForConfig(c)
	if err != nil {
		log.Info("isOpenShift discovery.NewDiscoveryClientForConfig has encountered a problem")
		return false
	}
	apiList, err := dcclient.ServerGroups()
	if err != nil {
		log.Info("isOpenShift client.ServerGroups has encountered a problem")
		return false
	}
	for _, v := range apiList.Groups {
		log.Info(v.Name)
		if v.Name == "route.openshift.io" {

			log.Info("route.openshift.io was found in apis, platform is OpenShift")
			return true
		}
	}
	return false
}

func (r *WebServerReconciler) getWebServer(request reconcile.Request) (*webserversv1alpha1.WebServer, error) {
	webServer := &webserversv1alpha1.WebServer{}
	err := r.Client.Get(context.TODO(), request.NamespacedName, webServer)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			log.Info("WebServer resource not found. Ignoring since object must have been deleted")
			return webServer, err
		}
		// Error reading the object - requeue the request.
		log.Error(err, "Failed to get WebServer resource")
		return webServer, err
	}
	return webServer, nil
}

func (r *WebServerReconciler) setDefaultValues(webServer *webserversv1alpha1.WebServer) *webserversv1alpha1.WebServer {

	if webServer.Spec.WebImage != nil && webServer.Spec.WebImage.WebApp != nil {
		webApp := webServer.Spec.WebImage.WebApp
		if webApp.Name == "" {
			log.Info("WebServer.Spec.Image.WebApp.Name is not set, setting value to 'ROOT'")
			webApp.Name = "ROOT"
		}
		if webApp.DeployPath == "" {
			log.Info("WebServer.Spec.Image.WebApp.DeployPath is not set, setting value to '/deployments/'")
			webApp.DeployPath = "/deployments/"
		}

		if webApp.Builder.ApplicationBuildScript == "" {
			log.Info("WebServer.Spec.Image.WebApp.Builder.ApplicationBuildScript is not set, will use the default build script")
		}
		if webApp.WebAppWarImagePushSecret == "" {
			log.Info("WebServer.Spec.Image.WebApp.WebAppWarImagePushSecret is not set!!!")
		}
	}

	return webServer

}

func (r *WebServerReconciler) generateWebAppBuildScript(webServer *webserversv1alpha1.WebServer) string {
	webApp := webServer.Spec.WebImage.WebApp
	webAppWarFileName := webApp.Name + ".war"
	webAppSourceRepositoryURL := webApp.SourceRepositoryURL
	webAppSourceRepositoryRef := webApp.SourceRepositoryRef
	webAppSourceRepositoryContextDir := webApp.SourceRepositoryContextDir

	return fmt.Sprintf(`
		webAppWarFileName=%s;
		webAppSourceRepositoryURL=%s;
		webAppSourceRepositoryRef=%s;
		webAppSourceRepositoryContextDir=%s;

		# Some pods don't have root privileges, so the build takes place in /tmp
		cd tmp;

		# Create a custom .m2 repo in a location where no root privileges are required
		mkdir -p /tmp/.m2/repo;

		# Create custom maven settings that change the location of the .m2 repo
		echo '<settings xmlns="http://maven.apache.org/SETTINGS/1.0.0" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"' >> /tmp/.m2/settings.xml
		echo 'xsi:schemaLocation="http://maven.apache.org/SETTINGS/1.0.0 https://maven.apache.org/xsd/settings-1.0.0.xsd">' >> /tmp/.m2/settings.xml
		echo '<localRepository>/tmp/.m2/repo</localRepository>' >> /tmp/.m2/settings.xml
		echo '</settings>' >> /tmp/.m2/settings.xml

		if [ -z ${webAppSourceRepositoryURL} ]; then
			echo "Need an URL like https://github.com/jfclere/demo-webapp.git";
			exit 1;
		fi;

		git clone ${webAppSourceRepositoryURL};
		if [ $? -ne 0 ]; then
			echo "Can't clone ${webAppSourceRepositoryURL}";
			exit 1;
		fi;

		# Get the name of the source code directory
		DIR=$(echo ${webAppSourceRepositoryURL##*/});
		DIR=$(echo ${DIR%%.*});

		cd ${DIR};

		if [ ! -z ${webAppSourceRepositoryRef} ]; then
			git checkout ${webAppSourceRepositoryRef};
		fi;

		if [ ! -z ${webAppSourceRepositoryContextDir} ]; then
			cd ${webAppSourceRepositoryContextDir};
		fi;

		# Builds the webapp using the custom maven settings
		mvn clean install -gs /tmp/.m2/settings.xml;
		if [ $? -ne 0 ]; then
			echo "mvn install failed please check the pom.xml in ${webAppSourceRepositoryURL}";
			exit 1;
		fi

		# Copies the resulting war to the mounted persistent volume
		cp target/*.war /mnt/${webAppWarFileName};`,
		webAppWarFileName,
		webAppSourceRepositoryURL,
		webAppSourceRepositoryRef,
		webAppSourceRepositoryContextDir,
	)
}

func (r *WebServerReconciler) createService(webServer *webserversv1alpha1.WebServer, resource *corev1.Service, resourceName string, resourceNamespace string) (ctrl.Result, error) {
	err := r.Client.Get(context.TODO(), client.ObjectKey{
		Namespace: resourceNamespace,
		Name:      resourceName,
	}, resource)
	if err != nil && errors.IsNotFound(err) {
		// Create a new resource
		log.Info("Creating a new Service: " + resourceName + " Namespace: " + resourceNamespace)
		err = r.Client.Create(context.TODO(), resource)
		if err != nil && !errors.IsAlreadyExists(err) {
			log.Error(err, "Failed to create a new Service: "+resourceName+" Namespace: "+resourceNamespace)
			return reconcile.Result{}, err
		}
		// Resource created successfully - return and requeue
		// return ctrl.Result{Requeue: true}, err
		return ctrl.Result{Requeue: true}, nil
	} else if err != nil {
		log.Error(err, "Failed to get Service: "+resourceName)
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, err
}

// Test for the "view" RoleBinding and if not existing try to create it, if that fails we can't use useKUBEPing

func (r *WebServerReconciler) createRoleBinding(webServer *webserversv1alpha1.WebServer, resource *rbac.RoleBinding, resourceName string, resourceNamespace string) (ctrl.Result, error) {
	err := r.Client.Get(context.TODO(), client.ObjectKey{
		Namespace: resourceNamespace,
		Name:      resourceName,
	}, resource)
	if err != nil && errors.IsNotFound(err) {
		// Create a new resource
		log.Info("Creating a new RoleBinding: " + resourceName + " Namespace: " + resourceNamespace)
		err = r.Client.Create(context.TODO(), resource)
		if err != nil && !errors.IsAlreadyExists(err) {
			log.Error(err, "Failed to create a new RoleBinding: "+resourceName+" Namespace: "+resourceNamespace)
			r.useKUBEPing = false
			return reconcile.Result{}, err
		}
		// Resource created successfully - return and requeue
		// return ctrl.Result{Requeue: true}, err
		return ctrl.Result{Requeue: true}, nil
	} else if err != nil {
		log.Error(err, "Failed to get RoleBinding "+resourceName)
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, err
}

func (r *WebServerReconciler) createConfigMap(webServer *webserversv1alpha1.WebServer, resource *corev1.ConfigMap, resourceName string, resourceNamespace string) (ctrl.Result, error) {
	err := r.Client.Get(context.TODO(), client.ObjectKey{
		Namespace: resourceNamespace,
		Name:      resourceName,
	}, resource)
	if err != nil && errors.IsNotFound(err) {
		// Create a new resource
		log.Info("Creating a new ConfigMap: " + resourceName + " Namespace: " + resourceNamespace)
		err = r.Client.Create(context.TODO(), resource)
		if err != nil && !errors.IsAlreadyExists(err) {
			log.Error(err, "Failed to create a new ConfigMap: "+resourceName+" Namespace: "+resourceNamespace)
			return reconcile.Result{}, err
		}
		// Resource created successfully - return and requeue
		// return ctrl.Result{Requeue: true}, err
		return ctrl.Result{Requeue: true}, nil
	} else if err != nil {
		log.Error(err, "Failed to get ConfigMap "+resourceName)
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, err
}

func (r *WebServerReconciler) createPod(webServer *webserversv1alpha1.WebServer, resource *corev1.Pod, resourceName string, resourceNamespace string) (ctrl.Result, error) {
	err := r.Client.Get(context.TODO(), client.ObjectKey{
		Namespace: resourceNamespace,
		Name:      resourceName,
	}, resource)
	if err != nil && errors.IsNotFound(err) {
		// Create a new resource
		log.Info("Creating a new Pod: " + resourceName + " Namespace: " + resourceNamespace)
		err = r.Client.Create(context.TODO(), resource)
		if err != nil && !errors.IsAlreadyExists(err) {
			log.Error(err, "Failed to create a new Pod: "+resourceName+" Namespace: "+resourceNamespace)
			return reconcile.Result{}, err
		}
		// Resource created successfully - return and requeue
		// return ctrl.Result{Requeue: true}, err
		return ctrl.Result{Requeue: true}, nil
	} else if err != nil {
		log.Error(err, "Failed to get Pod: "+resourceName)
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, err
}

func (r *WebServerReconciler) createDeployment(webServer *webserversv1alpha1.WebServer, resource *kbappsv1.Deployment, resourceName string, resourceNamespace string) (ctrl.Result, error) {
	err := r.Client.Get(context.TODO(), types.NamespacedName{Name: resourceName, Namespace: resourceNamespace}, resource)
	// err := r.Client.Get(context.TODO(), client.ObjectKey{
	// 	Namespace: resourceNamespace,
	// 	Name:      resourceName,
	// }, resource)
	if err != nil && errors.IsNotFound(err) {
		// Create a new resource
		log.Info("Creating a new Deployment: " + resourceName + " Namespace: " + resourceNamespace)
		err = r.Client.Create(context.TODO(), resource)
		if err != nil && !errors.IsAlreadyExists(err) {
			log.Error(err, "Failed to create a new Deployment: "+resourceName+" Namespace: "+resourceNamespace)
			return reconcile.Result{}, err
		}
		// Resource created successfully - return and requeue
		// return ctrl.Result{Requeue: true}, err
		return ctrl.Result{Requeue: true}, nil
	} else if err != nil {
		log.Error(err, "Failed to get Deployment: "+resourceName)
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, err
}

func (r *WebServerReconciler) createImageStream(webServer *webserversv1alpha1.WebServer, resource *imagev1.ImageStream, resourceName string, resourceNamespace string) (ctrl.Result, error) {
	err := r.Client.Get(context.TODO(), client.ObjectKey{
		Namespace: resourceNamespace,
		Name:      resourceName,
	}, resource)
	if err != nil && errors.IsNotFound(err) {
		// Create a new resource
		log.Info("Creating a new ImageStream: " + resourceName + " Namespace: " + resourceNamespace)
		err = r.Client.Create(context.TODO(), resource)
		if err != nil && !errors.IsAlreadyExists(err) {
			log.Error(err, "Failed to create a new ImageStream: "+resourceName+" Namespace: "+resourceNamespace)
			return reconcile.Result{}, err
		}
		// Resource created successfully - return and requeue
		// return ctrl.Result{Requeue: true}, err
		return ctrl.Result{Requeue: true}, nil
	} else if err != nil {
		log.Error(err, "Failed to get ImageStream: "+resourceName)
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, err
}

func (r *WebServerReconciler) createBuildConfig(webServer *webserversv1alpha1.WebServer, resource *buildv1.BuildConfig, resourceName string, resourceNamespace string) (ctrl.Result, error) {
	err := r.Client.Get(context.TODO(), client.ObjectKey{
		Namespace: resourceNamespace,
		Name:      resourceName,
	}, resource)
	if err != nil && errors.IsNotFound(err) {
		// Create a new resource
		log.Info("Creating a new BuildConfig: " + resourceName + " Namespace: " + resourceNamespace)
		err = r.Client.Create(context.TODO(), resource)
		if err != nil && !errors.IsAlreadyExists(err) {
			log.Error(err, "Failed to create a new BuildConfig: "+resourceName+" Namespace: "+resourceNamespace)
			return reconcile.Result{}, err
		}
		// Resource created successfully - return and requeue
		// return ctrl.Result{Requeue: true}, err
		return ctrl.Result{Requeue: true}, nil
	} else if err != nil {
		log.Error(err, "Failed to get BuildConfig: "+resourceName)
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, err
}

func (r *WebServerReconciler) createDeploymentConfig(webServer *webserversv1alpha1.WebServer, resource *appsv1.DeploymentConfig, resourceName string, resourceNamespace string) (ctrl.Result, error) {
	err := r.Client.Get(context.TODO(), client.ObjectKey{
		Namespace: resourceNamespace,
		Name:      resourceName,
	}, resource)
	if err != nil && errors.IsNotFound(err) {
		// Create a new resource
		log.Info("Creating a new DeploymentConfig: " + resourceName + " Namespace: " + resourceNamespace)
		err = r.Client.Create(context.TODO(), resource)
		if err != nil && !errors.IsAlreadyExists(err) {
			log.Error(err, "Failed to create a new DeploymentConfig: "+resourceName+" Namespace: "+resourceNamespace)
			return reconcile.Result{}, err
		}
		// Resource created successfully - return and requeue
		// return ctrl.Result{Requeue: true}, err
		return ctrl.Result{Requeue: true}, nil
	} else if err != nil {
		log.Error(err, "Failed to get DeploymentConfig: "+resourceName)
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, err
}

func (r *WebServerReconciler) createRoute(webServer *webserversv1alpha1.WebServer, resource *routev1.Route, resourceName string, resourceNamespace string) (ctrl.Result, error) {
	err := r.Client.Get(context.TODO(), client.ObjectKey{
		Namespace: resourceNamespace,
		Name:      resourceName,
	}, resource)
	if err != nil && errors.IsNotFound(err) {
		// Create a new resource
		log.Info("Creating a new Route: " + resourceName + " Namespace: " + resourceNamespace)
		err = r.Client.Create(context.TODO(), resource)
		if err != nil && !errors.IsAlreadyExists(err) {
			log.Error(err, "Failed to create a new Route: "+resourceName+" Namespace: "+resourceNamespace)
			return reconcile.Result{}, err
		}
		// Resource created successfully - return and requeue
		// return ctrl.Result{Requeue: true}, err
		return ctrl.Result{Requeue: true}, nil
	} else if err != nil {
		log.Error(err, "Failed to get Route: "+resourceName)
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, err
}

func (r *WebServerReconciler) checkBuildPodPhase(buildPod *corev1.Pod) (reconcile.Result, error) {
	if buildPod.Status.Phase != corev1.PodSucceeded {
		switch buildPod.Status.Phase {
		case corev1.PodFailed:
			log.Info("Application build failed: " + buildPod.Status.Message)
		case corev1.PodPending:
			log.Info("Application build pending")
		case corev1.PodRunning:
			log.Info("Application is still being built")
		default:
			log.Info("Unknown build pod status")
		}
		return reconcile.Result{RequeueAfter: (5 * time.Second)}, nil
	}
	return reconcile.Result{}, nil
}

// getPodList lists pods which belongs to the Web server
// the pods are differentiated based on the selectors
func (r *WebServerReconciler) getPodList(webServer *webserversv1alpha1.WebServer) (*corev1.PodList, error) {
	podList := &corev1.PodList{}

	listOpts := []client.ListOption{
		client.InNamespace(webServer.Namespace),
		client.MatchingLabels(r.generateLabelsForWeb(webServer)),
	}
	err := r.Client.List(context.TODO(), podList, listOpts...)

	if err == nil {
		// sorting pods by number in the name
		r.sortPodListByName(podList)
	}
	return podList, err
}

// generateLabelsForWeb return a map of labels that are used for identification
//  of objects belonging to the particular WebServer instance
func (r *WebServerReconciler) generateLabelsForWeb(webServer *webserversv1alpha1.WebServer) map[string]string {
	labels := map[string]string{
		"deploymentConfig": webServer.Spec.ApplicationName,
		"WebServer":        webServer.Name,
	}
	// labels["app.kubernetes.io/name"] = webServer.Name
	// labels["app.kubernetes.io/managed-by"] = os.Getenv("LABEL_APP_MANAGED_BY")
	// labels["app.openshift.io/runtime"] = os.Getenv("LABEL_APP_RUNTIME")
	if webServer.Labels != nil {
		for labelKey, labelValue := range webServer.Labels {
			log.Info("labels: ", labelKey, " : ", labelValue)
			labels[labelKey] = labelValue
		}
	}
	return labels
}

// sortPodListByName sorts the pod list by number in the name
//  expecting the format which the StatefulSet works with which is `<podname>-<number>`
func (r *WebServerReconciler) sortPodListByName(podList *corev1.PodList) *corev1.PodList {
	sort.SliceStable(podList.Items, func(i, j int) bool {
		return podList.Items[i].ObjectMeta.Name < podList.Items[j].ObjectMeta.Name
	})
	return podList
}

// getPodStatus returns the pod names of the array of pods passed in
func (r *WebServerReconciler) getPodStatus(pods []corev1.Pod) ([]webserversv1alpha1.PodStatus, bool) {
	var requeue = false
	var podStatuses []webserversv1alpha1.PodStatus
	for _, pod := range pods {
		podState := webserversv1alpha1.PodStateFailed

		switch pod.Status.Phase {
		case corev1.PodPending:
			podState = webserversv1alpha1.PodStatePending
		case corev1.PodRunning:
			podState = webserversv1alpha1.PodStateActive
		}

		podStatuses = append(podStatuses, webserversv1alpha1.PodStatus{
			Name:  pod.Name,
			PodIP: pod.Status.PodIP,
			State: podState,
		})
		if pod.Status.PodIP == "" {
			requeue = true
		}
	}
	if requeue {
		log.Info("Some pods don't have an IP address yet, reconciliation requeue scheduled")
	}
	return podStatuses, requeue
}

// updateWebServerStatus updates status of the WebServer resource.
func (r *WebServerReconciler) updateWebServerStatus(webServer *webserversv1alpha1.WebServer, client client.Client, ctx context.Context) error {
	log.Info("Updating the status of WebServer")

	if err := r.Status().Update(ctx, webServer); err != nil {
		log.Error(err, "Failed to update the status of WebServer")
		return err
	}

	log.Info("The status of WebServer was updated successfully")
	return nil
}
