package controller

import (
	"context"

	kappsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	appsv1 "github.com/abit2/kaam/api/v1"
)

func (r *JobReconciler) createStatefulSet(ctx context.Context, job appsv1.Job) (*kappsv1.StatefulSet, error) {
	logger := logf.FromContext(ctx)

	logger.Info("Creating StatefulSet", "namespace", job.Namespace, "name", job.Name)

	volumeName := "pod" + job.Spec.Volume.Name
	replica := job.Spec.Replicas
	statefulSet := kappsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "stateful-" + job.Name,
			Namespace: job.Namespace,
		},
		Spec: kappsv1.StatefulSetSpec{
			Replicas: &replica,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": "busybox",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": "busybox",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "busybox",
							Image: "quay.io/prometheus/busybox:latest",
							Args: []string{
								"/bin/sh",
								"-c",
								"echo Hello Kubernetes! && sleep 3600",
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      volumeName,
									ReadOnly:  false,
									MountPath: "/tmp/mydata",
								},
							},
						},
					},
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: volumeName,
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						AccessModes: []corev1.PersistentVolumeAccessMode{
							corev1.ReadWriteOnce,
						},
						Resources: corev1.VolumeResourceRequirements{
							Limits: corev1.ResourceList{
								corev1.ResourceStorage: resource.MustParse(job.Spec.Volume.Storage),
							},
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: resource.MustParse(job.Spec.Volume.Storage),
							},
						},
					},
				},
			},
			ServiceName:         "service",
			PodManagementPolicy: kappsv1.ParallelPodManagement,
		},
	}

	// Set the owner reference to the Job
	if err := ctrl.SetControllerReference(&job, &statefulSet, r.Scheme); err != nil {
		return nil, err
	}

	return &statefulSet, nil
}
