// agent_operator.go - Enterprise AI Agent Kubernetes Operator
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	aiv1alpha1 "github.com/Wavine-ai/operator/api/v1alpha1"
)

const (
	agentFinalizer   = "finalizer.agents.cirium.ai"
	requeueDelay     = 10 * time.Second
	maxConcurrent    = 5
	agentVersionKey  = "agent.Wavine.ai/version"
	configHashKey    = "agent.Wavine.ai/config-hash"
)

// AgentReconciler manages the lifecycle of AIAgent resources
type AgentReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=ai.nuzon.io,resources=aiagents,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ai.nuzon.io,resources=aiagents/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services;configmaps;secrets,verbs=get;list;watch;create;update;patch;delete

func main() {
	opts := zap.Options{
		Development: false,
	}
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 runtime.NewScheme(),
		MetricsBindAddress:     ":8080",
		Port:                   9443,
		LeaderElection:         true,
		LeaderElectionID:       "nuzon-agent-operator",
		SyncPeriod:             durationPtr(5 * time.Minute),
		HealthProbeBindAddress: ":8081",
	})
	if err != nil {
		setupLog.Error(err, "failed to start manager")
		os.Exit(1)
	}

	if err = (&AgentReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("agent-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "failed to create controller", "controller", "AIAgent")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func (r *AgentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&aiv1alpha1.AIAgent{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: maxConcurrent}).
		Complete(r)
}

func (r *AgentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	
	var agent aiv1alpha1.AIAgent
	if err := r.Get(ctx, req.NamespacedName, &agent); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Handle finalization
	if !agent.DeletionTimestamp.IsZero() {
		return r.finalizeAgent(ctx, &agent)
	}

	// Add finalizer if missing
	if !containsString(agent.Finalizers, agentFinalizer) {
		agent.Finalizers = append(agent.Finalizers, agentFinalizer)
		if err := r.Update(ctx, &agent); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Reconciliation logic
	result, err := r.reconcileAgent(ctx, &agent)
	if err != nil {
		log.Error(err, "Reconciliation failed")
		r.Recorder.Event(&agent, corev1.EventTypeWarning, "ReconcileError", err.Error())
	}
	return result, err
}

func (r *AgentReconciler) reconcileAgent(ctx context.Context, agent *aiv1alpha1.AIAgent) (ctrl.Result, error) {
	// Configuration management
	configHash, err := r.ensureConfigMap(ctx, agent)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to manage config: %w", err)
	}

	// Deployment management
	if err := r.ensureDeployment(ctx, agent, configHash); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to manage deployment: %w", err)
	}

	// Service exposure
	if err := r.ensureService(ctx, agent); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to manage service: %w", err)
	}

	// Update status
	if err := r.updateAgentStatus(ctx, agent); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update status: %w", err)
	}

	return ctrl.Result{RequeueAfter: requeueDelay}, nil
}

func (r *AgentReconciler) ensureDeployment(ctx context.Context, agent *aiv1alpha1.AIAgent, configHash string) error {
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agent.Name,
			Namespace: agent.Namespace,
			Labels:    agentLabels(agent),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: agent.Spec.Replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: agentLabels(agent),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      withConfigHash(agentLabels(agent), configHash),
					Annotations: podAnnotations(agent),
				},
				Spec: corev1.PodSpec{
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: ptrBool(true),
						RunAsUser:    ptrInt64(1000),
						FSGroup:      ptrInt64(2000),
					},
					Containers: []corev1.Container{{
						Name:            "agent",
						Image:           agent.Spec.Image,
						ImagePullPolicy: corev1.PullIfNotPresent,
						Resources:       agent.Spec.Resources,
						EnvFrom: []corev1.EnvFromSource{{
							ConfigMapRef: &corev1.ConfigMapEnvSource{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: agent.Name + "-config",
								},
							},
						}},
						LivenessProbe:  healthProbe(),
						ReadinessProbe: healthProbe(),
						SecurityContext: &corev1.SecurityContext{
							Capabilities: &corev1.Capabilities{
								Drop: []corev1.Capability{"ALL"},
							},
							ReadOnlyRootFilesystem: ptrBool(true),
						},
					}},
					Tolerations:        agent.Spec.Tolerations,
					NodeSelector:      agent.Spec.NodeSelector,
					Affinity:          agent.Spec.Affinity,
					PriorityClassName: agent.Spec.PriorityClassName,
				},
			},
		},
	}

	// Set ownership reference
	if err := ctrl.SetControllerReference(agent, deploy, r.Scheme); err != nil {
		return err
	}

	// Apply deployment
	existingDeploy := &appsv1.Deployment{}
	err := r.Get(ctx, types.NamespacedName{Name: deploy.Name, Namespace: deploy.Namespace}, existingDeploy)
	if err != nil && apierrors.IsNotFound(err) {
		if err := r.Create(ctx, deploy); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		deploy.ResourceVersion = existingDeploy.ResourceVersion
		if err := r.Update(ctx, deploy); err != nil {
			return err
		}
	}
	return nil
}

// Helper functions and remaining implementation...
