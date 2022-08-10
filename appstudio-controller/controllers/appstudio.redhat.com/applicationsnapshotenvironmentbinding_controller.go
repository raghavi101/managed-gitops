/*
Copyright 2022.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package appstudioredhatcom

import (
	"context"
	"fmt"
	"reflect"
	"time"

	appstudioshared "github.com/redhat-appstudio/managed-gitops/appstudio-shared/apis/appstudio.redhat.com/v1alpha1"
	apibackend "github.com/redhat-appstudio/managed-gitops/backend-shared/apis/managed-gitops/v1alpha1"
	sharedutil "github.com/redhat-appstudio/managed-gitops/backend-shared/util"
	apierr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// ApplicationSnapshotEnvironmentBindingReconciler reconciles a ApplicationSnapshotEnvironmentBinding object
type ApplicationSnapshotEnvironmentBindingReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=appstudio.redhat.com,resources=applicationsnapshotenvironmentbindings,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=appstudio.redhat.com,resources=applicationsnapshotenvironmentbindings/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=appstudio.redhat.com,resources=applicationsnapshotenvironmentbindings/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.11.0/pkg/reconcile
func (r *ApplicationSnapshotEnvironmentBindingReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {

	log := log.FromContext(ctx).WithValues("name", req.Name, "namespace", req.Namespace)
	defer log.V(sharedutil.LogLevel_Debug).Info("Application Snapshot Environment Binding Reconcile() complete.")

	binding := &appstudioshared.ApplicationSnapshotEnvironmentBinding{}

	if err := r.Client.Get(ctx, req.NamespacedName, binding); err != nil {
		// Binding doesn't exist: it was deleted.
		// Owner refs will ensure the GitOpsDeployments are deleted, so no work to do.
		return ctrl.Result{}, nil
	}

	environment := appstudioshared.Environment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      binding.Spec.Environment,
			Namespace: req.Namespace,
		},
	}
	if err := r.Client.Get(ctx, client.ObjectKeyFromObject(&environment), &environment); err != nil {
		if apierr.IsNotFound(err) {
			return ctrl.Result{}, nil
		} else {
			return ctrl.Result{}, fmt.Errorf("unable to retrieve Environment '%s' referenced by Binding: %v", environment.Name, err)
		}
	}

	// Don't reconcile the binding if the HAS component indicated via the binding.status field
	// that there were issues with the GitOps repository, or if the GitOps repository isn't ready
	// yet.

	if len(binding.Status.GitOpsRepoConditions) > 0 &&
		binding.Status.GitOpsRepoConditions[len(binding.Status.GitOpsRepoConditions)-1].Status == metav1.ConditionFalse {
		// if the ApplicationSnapshotEventBinding GitOps Repo Conditions status is false - return;
		// since there was an unexpected issue with refreshing/syncing the GitOps repository
		log.V(sharedutil.LogLevel_Debug).Info("Can not Reconcile Binding '" + binding.Name + "', since GitOps Repo Conditions status is false.")
		// TODO: GITOPSRVCE-182: Add this to .status.conditions field once it is implemented.
		return ctrl.Result{}, nil

	} else if len(binding.Status.Components) == 0 {

		log.V(sharedutil.LogLevel_Debug).Info("ApplicationSnapshotEventBinding Component status is required to "+
			"generate GitOps deployment, waiting for the Application Service controller to finish reconciling binding", "bindingName", binding.Name)

		// TODO: GITOPSRVCE-182: Add this to .status.conditions field once it is implemented.

		// if length of the Binding component status is 0 and there is no issue with the GitOps Repo Conditions;
		// the Application Service controller has not synced the GitOps repository yet, return and requeue.
		return ctrl.Result{}, nil
	}

	// map: componentName (string) -> expected GitOpsDeployment for that component name
	expectedDeployments := map[string]apibackend.GitOpsDeployment{}

	for _, component := range binding.Status.Components {

		// sanity test that there are no duplicate components by name
		if _, exists := expectedDeployments[component.Name]; exists {
			// TODO: GITOPSRVCE-182: Add this to .status.conditions field once it is implemented.
			log.Error(nil, fmt.Sprintf("%s: %s", errDuplicateKeysFound, component.Name))
			return ctrl.Result{}, nil
		}

		var err error
		expectedDeployments[component.Name], err = generateExpectedGitOpsDeployment(component, *binding, environment)
		if err != nil {
			return ctrl.Result{RequeueAfter: time.Second * 10}, fmt.Errorf("invalid target namespace: %v", err)
		}
	}

	statusField := []appstudioshared.BindingStatusGitOpsDeployment{}
	var allErrors error

	// For each deployment, check if it exists, and if it has the expected content.
	// - If not, create/update it.
	for componentName, expectedGitOpsDeployment := range expectedDeployments {

		if err := processExpectedGitOpsDeployment(ctx, expectedGitOpsDeployment, *binding, r.Client); err != nil {

			errorMessage := fmt.Sprintf("Error occurred while processing expected GitOpsDeployment '%s' for Binding '%s'",
				expectedGitOpsDeployment.Name, binding.Name)
			log.Error(err, errorMessage)

			// Combine all errors that occurred in the loop
			if allErrors == nil {
				allErrors = fmt.Errorf("%s, error: %w", errorMessage, err)
			} else {
				allErrors = fmt.Errorf("%s.\n%s, error: %w", allErrors.Error(), errorMessage, err)
			}
		} else {
			// No error: add to status
			statusField = append(statusField, appstudioshared.BindingStatusGitOpsDeployment{
				ComponentName:    componentName,
				GitOpsDeployment: expectedGitOpsDeployment.Name,
			})
		}
	}

	// Update the status field with statusField vars (even if an error occurred)
	binding.Status.GitOpsDeployments = statusField
	sharedutil.LogAPIResourceChangeEvent(binding.Namespace, binding.Name, binding, sharedutil.ResourceModified, log)
	if err := r.Client.Status().Update(ctx, binding); err != nil {
		if apierr.IsNotFound(err) {
			return ctrl.Result{}, nil
		}

		log.Error(err, "unable to update gitopsdeployments status for Binding "+binding.Name)
		return ctrl.Result{}, fmt.Errorf("unable to update gitopsdeployments status for Binding %s. Error: %w", binding.Name, err)
	}

	if allErrors != nil {
		return ctrl.Result{RequeueAfter: time.Second * 10}, fmt.Errorf("unable to process expected GitOpsDeployment: %w", allErrors)
	}

	return ctrl.Result{}, nil
}

const (
	errDuplicateKeysFound     = "duplicate component keys found in status field"
	errMissingTargetNamespace = "TargetNamespace field of Environment was empty"
)

// processExpectedGitOpsDeployment processed the GitOpsDeployment that is expected for a particular Component
func processExpectedGitOpsDeployment(ctx context.Context, expectedGitopsDeployment apibackend.GitOpsDeployment,
	binding appstudioshared.ApplicationSnapshotEnvironmentBinding, k8sClient client.Client) error {

	log := log.FromContext(ctx).WithValues("binding", binding.Name, "gitOpsDeployment", expectedGitopsDeployment.Name, "namespace", binding.Namespace)
	actualGitOpsDeployment := apibackend.GitOpsDeployment{}

	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(&expectedGitopsDeployment), &actualGitOpsDeployment); err != nil {

		// A) If the GitOpsDeployment doesn't exist, create it
		if !apierr.IsNotFound(err) {
			log.Error(err, "expectedGitopsDeployment: "+expectedGitopsDeployment.Name+" not found for Binding "+binding.Name)
			return fmt.Errorf("expectedGitopsDeployment: %s not found for Binding: %s: Error: %w", expectedGitopsDeployment.Name, binding.Name, err)
		}
		sharedutil.LogAPIResourceChangeEvent(expectedGitopsDeployment.Namespace, expectedGitopsDeployment.Name, expectedGitopsDeployment, sharedutil.ResourceCreated, log)
		if err := k8sClient.Create(ctx, &expectedGitopsDeployment); err != nil {
			log.Error(err, "unable to create expectedGitopsDeployment: '"+expectedGitopsDeployment.Name+"' for Binding: '"+binding.Name+"'")
			return err
		}

		return nil
	}

	// GitOpsDeployment already exists, so compare it with what we expect
	if reflect.DeepEqual(expectedGitopsDeployment.Spec, actualGitOpsDeployment.Spec) {
		// B) The GitOpsDeployment is exactly as expected, so return
		return nil
	}

	// C) The GitOpsDeployment should be updated to be consistent with what we expect
	actualGitOpsDeployment.Spec = expectedGitopsDeployment.Spec
	sharedutil.LogAPIResourceChangeEvent(expectedGitopsDeployment.Namespace, expectedGitopsDeployment.Name, expectedGitopsDeployment, sharedutil.ResourceModified, log)
	if err := k8sClient.Update(ctx, &actualGitOpsDeployment); err != nil {
		log.Error(err, "unable to update actualGitOpsDeployment: "+actualGitOpsDeployment.Name+" for Binding: "+binding.Name)
		return fmt.Errorf("unable to update actualGitOpsDeployment '%s', for Binding:%s, Error: %w", actualGitOpsDeployment.Name, binding.Name, err)
	}

	return nil
}

// GenerateBindingGitOpsDeploymentName generates the name that will be used for a given GitOpsDeployment of a binding
func GenerateBindingGitOpsDeploymentName(binding appstudioshared.ApplicationSnapshotEnvironmentBinding, componentName string) string {

	expectedName := binding.Name + "-" + binding.Spec.Application + "-" + binding.Spec.Environment + "-" + componentName

	// If the length of the GitOpsDeployment exceeds the K8s maximum, shorten it to just binding+component
	if len(expectedName) > 250 {
		expectedName = binding.Name + "-" + componentName
	}
	// TODO: GITOPSRVCE-183: Improve the logic here; it is not guaranteed that the updated name will be valid (plus add tests).

	return expectedName

}

func generateExpectedGitOpsDeployment(component appstudioshared.ComponentStatus,
	binding appstudioshared.ApplicationSnapshotEnvironmentBinding, environment appstudioshared.Environment) (apibackend.GitOpsDeployment, error) {

	res := apibackend.GitOpsDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      GenerateBindingGitOpsDeploymentName(binding, component.Name),
			Namespace: binding.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: binding.APIVersion,
					Kind:       binding.Kind,
					Name:       binding.Name,
					UID:        binding.UID,
				},
			},
		},
		Spec: apibackend.GitOpsDeploymentSpec{
			Source: apibackend.ApplicationSource{
				RepoURL:        component.GitOpsRepository.URL,
				Path:           component.GitOpsRepository.Path,
				TargetRevision: component.GitOpsRepository.Branch,
			},
			Type:        apibackend.GitOpsDeploymentSpecType_Automated, // Default to automated, for now
			Destination: apibackend.ApplicationDestination{},           // Default to same namespace, for now
		},
	}

	// If the environment has a target cluster field defined, then set the destination to that managed environment
	if environment.Spec.UnstableConfigurationFields != nil {

		managedEnvironmentName := generateEmptyManagedEnvironment(environment.Name, environment.Namespace).Name

		if environment.Spec.UnstableConfigurationFields.TargetNamespace == "" {
			return apibackend.GitOpsDeployment{}, fmt.Errorf("%s: '%s'", errMissingTargetNamespace, environment.Name)
		}

		res.Spec.Destination = apibackend.ApplicationDestination{
			Environment: managedEnvironmentName,
			Namespace:   environment.Spec.UnstableConfigurationFields.TargetNamespace,
		}
	}

	return res, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ApplicationSnapshotEnvironmentBindingReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		// Uncomment the following line adding a pointer to an instance of the controlled resource as an argument
		For(&appstudioshared.ApplicationSnapshotEnvironmentBinding{}).
		Owns(&apibackend.GitOpsDeployment{}).
		Complete(r)
}
