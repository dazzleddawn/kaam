/*
Copyright 2025.

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

package controller

import (
	"context"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	jobsv1 "github.com/abit2/kaam/api/v1"
)

const drainLabel = "drain-job"

// JobReconciler reconciles a Job object
type JobReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=jobs.abit2.com,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=jobs.abit2.com,resources=jobs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=jobs.abit2.com,resources=jobs/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Job object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.22.1/pkg/reconcile
func (r *JobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)

	logger.Info("Reconciling Job", "namespace", req.Namespace, "name", req.Name)

	// get the job for the namespace
	var job jobsv1.Job
	if err := r.Get(ctx, req.NamespacedName, &job); err != nil {
		logger.Error(err, "err getting job resource")
		// ignore as the resource is not found
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	statefulSet, err := r.createStatefulSet(ctx, job)
	if err != nil {
		logger.Error(err, "err creating statefulset")
		return ctrl.Result{}, err
	}

	err = r.handleCleanupPVCs(ctx, req)
	if err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	if err := r.handleOrphanedPVCs(ctx, req, job.Spec.Replicas, job); err != nil {
		logger.Error(err, "err in check and drain")
	}

	if err := r.Create(ctx, statefulSet); err != nil {
		if apierrors.IsAlreadyExists(err) {
			updateErr := r.Update(ctx, statefulSet)
			if updateErr != nil {
				return ctrl.Result{}, updateErr
			}
			return ctrl.Result{}, nil
		}
		logger.Error(err, "err creating statefulset")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *JobReconciler) handleCleanupPVCs(ctx context.Context, req ctrl.Request) error {
	namespace := req.Namespace

	logger := logf.FromContext(ctx)
	var drainJobs batchv1.JobList
	if err := r.List(ctx, &drainJobs, client.InNamespace(namespace), client.MatchingLabels{"app": "drain-job"}); err != nil {
		return err
	}

	// 4. Check drain job statuses
	for _, j := range drainJobs.Items {
		if isJobComplete(&j) {
			pvcName := j.Labels["pvc-name"]
			logger.Info("Drain job %s completed, deleting PVC %s\n", j.Name, pvcName)

			err := r.Delete(ctx, &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      pvcName,
					Namespace: namespace,
				},
			})
			if err != nil && !apierrors.IsNotFound(err) {
				logger.Error(err, "Error deleting PVC %s", err)
				return err
			}

			// Optionally delete the job too
			if err := r.Delete(ctx, &j); err != nil && !apierrors.IsNotFound(err) {
				logger.Error(err, "Error deleting drain job %s\n", j.Name)
				return err
			}
		}
	}
	return nil
}

func isJobComplete(job *batchv1.Job) bool {
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobComplete && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func (r *JobReconciler) handleOrphanedPVCs(ctx context.Context, req ctrl.Request, desiredReplicas int32, job jobsv1.Job) error {
	logger := log.FromContext(ctx)
	// 1. List all pods owned by this job
	podList := &corev1.PodList{}
	if err := r.List(ctx, podList, client.InNamespace(req.Namespace)); err != nil {
		return err
	}

	// 2. Track PVCs that are still in use
	usedPVCs := map[string]bool{}
	for _, pod := range podList.Items {
		for _, vol := range pod.Spec.Volumes {
			if vol.PersistentVolumeClaim != nil {
				usedPVCs[vol.PersistentVolumeClaim.ClaimName] = true
			}
		}
	}

	// 3. List all PVCs associated with this job (using label selector or naming convention)
	pvcList := &corev1.PersistentVolumeClaimList{}
	// labelSelector := labels.SelectorFromSet(labels.Set{"job-name": jobName})
	if err := r.List(ctx, pvcList, client.InNamespace(req.Namespace)); err != nil {
		return err
	}

	if !(int(desiredReplicas) < len(pvcList.Items)) {
		logger.Info("desired replicas = pvcs")
		return nil
	}

	// 4. Find orphaned PVCs (PVCs not referenced by any pod)
	for _, pvc := range pvcList.Items {
		if _, ok := usedPVCs[pvc.Name]; !ok {
			logger.Info("Orphaned PVC detected", "name", pvc.Name)
			// 5. Trigger your drain job here
			if err := r.triggerDrainJob(ctx, req.Namespace, pvc.Name, job); err != nil {
				return err
			}
		}
	}

	return nil
}

// Example drain job trigger
func (r *JobReconciler) triggerDrainJob(ctx context.Context, namespace, pvcName string, job jobsv1.Job) error {
	// You can create a Kubernetes Job or call any custom logic
	log := log.FromContext(ctx)
	log.Info("Triggering drain job for PVC", "pvc", pvcName, "namespace", namespace)
	drainJobName := fmt.Sprintf("drain-%s-%d", pvcName, time.Now().Unix())

	// Check if the job already exists
	existingJob := &batchv1.Job{}
	err := r.Get(ctx, types.NamespacedName{Name: drainJobName, Namespace: namespace}, existingJob)
	if err == nil {
		// If job exists and succeeded, skip
		for _, c := range existingJob.Status.Conditions {
			if c.Type == batchv1.JobComplete && c.Status == corev1.ConditionTrue {
				log.Info("Drain job already completed", "job", drainJobName)
				return nil
			}
		}
		log.Info("Drain job already running or pending", "name", drainJobName)
		return nil
	}

	// Define the drain job spec
	drainJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      drainJobName,
			Namespace: namespace,
			Labels: map[string]string{
				"app":      drainLabel,
				"pvc-name": pvcName,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &job.Spec.Drain.BackoffLimit, // retry a few times on failure
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyOnFailure,
					Volumes: []corev1.Volume{
						{
							Name: "target-volume",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: pvcName,
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name:    "drain",
							Image:   "busybox:latest",
							Command: []string{"/bin/sh", "-c"},
							Args: []string{
								// Example cleanup command
								"echo 'Draining data from /tmp/mydata...'; sleep 5; echo 'Done.'",
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "target-volume",
									MountPath: "/tmp/mydata",
								},
							},
						},
					},
				},
			},
		},
	}

	if err := ctrl.SetControllerReference(&job, drainJob, r.Scheme); err != nil {
		return err
	}

	log.Info("Creating drain job for PVC", "job", drainJobName, "pvc name", pvcName)
	return r.Create(ctx, drainJob)
}

// SetupWithManager sets up the controller with the Manager.
func (r *JobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&jobsv1.Job{}).
		Owns(&batchv1.Job{}).
		Named("job").
		Complete(r)
}
