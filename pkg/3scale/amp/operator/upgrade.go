package operator

import (
	"context"
	"fmt"
	"reflect"

	"github.com/3scale/3scale-operator/apis/apps/v1alpha1"
	appsv1alpha1 "github.com/3scale/3scale-operator/apis/apps/v1alpha1"
	"github.com/3scale/3scale-operator/pkg/common"
	"github.com/3scale/3scale-operator/pkg/helper"
	"github.com/3scale/3scale-operator/pkg/reconcilers"

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"
	appsv1 "github.com/openshift/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type UpgradeApiManager struct {
	*reconcilers.BaseReconciler
	apiManager *appsv1alpha1.APIManager
	logger     logr.Logger
}

func NewUpgradeApiManager(b *reconcilers.BaseReconciler, apiManager *appsv1alpha1.APIManager) *UpgradeApiManager {
	return &UpgradeApiManager{
		BaseReconciler: b,
		apiManager:     apiManager,
		logger:         b.Logger().WithValues("APIManager Upgrade Controller", apiManager.Name),
	}
}

func (u *UpgradeApiManager) Upgrade() (reconcile.Result, error) {
	res, err := u.upgradeImages()
	if err != nil {
		return res, fmt.Errorf("Upgrading images: %w", err)
	}
	if res.Requeue {
		return res, nil
	}

	// This method should be called in all upgrade procedures between releases,
	// as some labels have as value the new release
	res, err = u.upgradePodTemplateLabels()
	if err != nil {
		return res, fmt.Errorf("Upgrading pod template labels: %w", err)
	}
	if res.Requeue {
		return res, nil
	}

	return reconcile.Result{}, nil
}

func (u *UpgradeApiManager) upgradeImages() (reconcile.Result, error) {
	res, err := u.upgradeAMPImageStreams()
	if res.Requeue || err != nil {
		return res, err
	}

	upgradeImages := sequentially(
		when(
			notExternal(v1alpha1.BackendRedis),
			(*UpgradeApiManager).upgradeBackendRedisImageStream,
		),
		when(
			notExternal(v1alpha1.SystemRedis),
			(*UpgradeApiManager).upgradeSystemRedisImageStream,
		),
		when(

			notExternal(v1alpha1.SystemDatabase),
			(*UpgradeApiManager).upgradeSystemDatabaseImageStream,
		),
	)

	res, err = upgradeImages(u)
	if res.Requeue || err != nil {
		return res, err
	}

	res, err = u.upgradeDeploymentConfigs()
	if res.Requeue || err != nil {
		return res, err
	}

	return reconcile.Result{}, nil
}

func (u *UpgradeApiManager) upgradeDeploymentConfigs() (reconcile.Result, error) {
	res, err := u.upgradeAPIcastDeploymentConfigs()
	if res.Requeue || err != nil {
		return res, err
	}

	res, err = u.upgradeBackendDeploymentConfigs()
	if res.Requeue || err != nil {
		return res, err
	}

	res, err = u.upgradeZyncDeploymentConfigs()
	if res.Requeue || err != nil {
		return res, err
	}

	res, err = u.upgradeMemcachedDeploymentConfig()
	if res.Requeue || err != nil {
		return res, err
	}

	res, err = u.upgradeSystemDeploymentConfigs()
	if res.Requeue || err != nil {
		return res, err
	}

	upgradeDCs := sequentially(
		when(
			notExternal(v1alpha1.BackendRedis),
			(*UpgradeApiManager).upgradeBackendRedisDeploymentConfig,
		),
		when(
			notExternal(v1alpha1.SystemRedis),
			(*UpgradeApiManager).upgradeSystemRedisDeploymentConfig,
		),
		when(
			notExternal(v1alpha1.SystemDatabase),
			(*UpgradeApiManager).upgradeSystemDatabaseDeploymentConfig,
		),
	)

	res, err = upgradeDCs(u)
	if res.Requeue || err != nil {
		return res, err
	}

	return reconcile.Result{}, nil
}

func (u *UpgradeApiManager) upgradeAPIcastDeploymentConfigs() (reconcile.Result, error) {
	apicast, err := Apicast(u.apiManager, u.Client())
	if err != nil {
		return reconcile.Result{}, err
	}

	res, err := u.upgradeDeploymentConfigImageChangeTrigger(apicast.StagingDeploymentConfig())
	if res.Requeue || err != nil {
		return res, err
	}

	res, err = u.upgradeDeploymentConfigImageChangeTrigger(apicast.ProductionDeploymentConfig())
	if res.Requeue || err != nil {
		return res, err
	}

	return reconcile.Result{}, nil
}

func (u *UpgradeApiManager) upgradeBackendDeploymentConfigs() (reconcile.Result, error) {
	backend, err := Backend(u.apiManager, u.Client())
	if err != nil {
		return reconcile.Result{}, err
	}

	res, err := u.upgradeDeploymentConfigImageChangeTrigger(backend.ListenerDeploymentConfig())
	if res.Requeue || err != nil {
		return res, err
	}

	res, err = u.upgradeDeploymentConfigImageChangeTrigger(backend.WorkerDeploymentConfig())
	if res.Requeue || err != nil {
		return res, err
	}

	res, err = u.upgradeDeploymentConfigImageChangeTrigger(backend.CronDeploymentConfig())
	if res.Requeue || err != nil {
		return res, err
	}

	return reconcile.Result{}, nil
}

func (u *UpgradeApiManager) upgradeZyncDeploymentConfigs() (reconcile.Result, error) {
	zync, err := Zync(u.apiManager, u.Client())
	if err != nil {
		return reconcile.Result{}, err
	}

	res, err := u.upgradeDeploymentConfigImageChangeTrigger(zync.DeploymentConfig())
	if res.Requeue || err != nil {
		return res, err
	}

	res, err = u.upgradeDeploymentConfigImageChangeTrigger(zync.QueDeploymentConfig())
	if res.Requeue || err != nil {
		return res, err
	}

	if !u.apiManager.IsExternal(v1alpha1.ZyncDatabase) {
		res, err = u.upgradeDeploymentConfigImageChangeTrigger(zync.DatabaseDeploymentConfig())
		if res.Requeue || err != nil {
			return res, err
		}
	}

	return reconcile.Result{}, nil
}

func (u *UpgradeApiManager) upgradeMemcachedDeploymentConfig() (reconcile.Result, error) {
	memcached, err := Memcached(u.apiManager)
	if err != nil {
		return reconcile.Result{}, err
	}

	res, err := u.upgradeDeploymentConfigImageChangeTrigger(memcached.DeploymentConfig())
	if res.Requeue || err != nil {
		return res, err
	}

	return reconcile.Result{}, nil
}
func (u *UpgradeApiManager) upgradeBackendRedisDeploymentConfig() (reconcile.Result, error) {
	redis, err := Redis(u.apiManager, u.Client())
	if err != nil {
		return reconcile.Result{}, err
	}
	desired := redis.BackendDeploymentConfig()

	existing := &appsv1.DeploymentConfig{}
	err = u.Client().Get(context.TODO(), types.NamespacedName{Name: desired.Name, Namespace: u.apiManager.Namespace}, existing)
	if err != nil {
		return reconcile.Result{}, err
	}

	changed := false
	tmpChanged, err := u.ensureDeploymentConfigImageChangeTrigger(desired, existing)
	if err != nil {
		return reconcile.Result{}, err
	}
	changed = changed || tmpChanged

	if changed {
		return reconcile.Result{Requeue: true}, u.UpdateResource(existing)
	}

	return reconcile.Result{}, nil
}

func (u *UpgradeApiManager) upgradeSystemRedisDeploymentConfig() (reconcile.Result, error) {
	redis, err := Redis(u.apiManager, u.Client())
	if err != nil {
		return reconcile.Result{}, err
	}
	desired := redis.SystemDeploymentConfig()

	existing := &appsv1.DeploymentConfig{}
	err = u.Client().Get(context.TODO(), types.NamespacedName{Name: desired.Name, Namespace: u.apiManager.Namespace}, existing)
	if err != nil {
		return reconcile.Result{}, err
	}

	changed := false
	tmpChanged, err := u.ensureDeploymentConfigImageChangeTrigger(desired, existing)
	if err != nil {
		return reconcile.Result{}, err
	}
	changed = changed || tmpChanged

	if changed {
		return reconcile.Result{Requeue: true}, u.UpdateResource(existing)
	}

	return reconcile.Result{}, nil
}

func (u *UpgradeApiManager) upgradeSystemDatabaseDeploymentConfig() (reconcile.Result, error) {
	if u.apiManager.IsSystemPostgreSQLEnabled() {
		return u.upgradeSystemPostgreSQLDeploymentConfig()
	}

	// default is MySQL
	return u.upgradeSystemMySQLDeploymentConfig()
}

func (u *UpgradeApiManager) upgradeSystemPostgreSQLDeploymentConfig() (reconcile.Result, error) {
	systemPostgreSQL, err := SystemPostgreSQL(u.apiManager, u.Client())
	if err != nil {
		return reconcile.Result{}, err
	}

	res, err := u.upgradeDeploymentConfigImageChangeTrigger(systemPostgreSQL.DeploymentConfig())
	if res.Requeue || err != nil {
		return res, err
	}

	return reconcile.Result{}, nil
}

func (u *UpgradeApiManager) upgradeSystemMySQLDeploymentConfig() (reconcile.Result, error) {
	systemMySQL, err := SystemMySQL(u.apiManager, u.Client())
	if err != nil {
		return reconcile.Result{}, err
	}

	res, err := u.upgradeDeploymentConfigImageChangeTrigger(systemMySQL.DeploymentConfig())
	if res.Requeue || err != nil {
		return res, err
	}

	return reconcile.Result{}, nil
}

func (u *UpgradeApiManager) upgradeSystemDeploymentConfigs() (reconcile.Result, error) {
	system, err := System(u.apiManager, u.Client())
	if err != nil {
		return reconcile.Result{}, err
	}

	res, err := u.upgradeDeploymentConfigImageChangeTrigger(system.AppDeploymentConfig())
	if res.Requeue || err != nil {
		return res, err
	}

	res, err = u.upgradeDeploymentConfigImageChangeTrigger(system.SidekiqDeploymentConfig())
	if res.Requeue || err != nil {
		return res, err
	}

	res, err = u.upgradeDeploymentConfigImageChangeTrigger(system.SphinxDeploymentConfig())
	if res.Requeue || err != nil {
		return res, err
	}

	return reconcile.Result{}, nil
}

func (u *UpgradeApiManager) upgradeDeploymentConfigImageChangeTrigger(desired *appsv1.DeploymentConfig) (reconcile.Result, error) {
	existing := &appsv1.DeploymentConfig{}
	err := u.Client().Get(context.TODO(), types.NamespacedName{Name: desired.Name, Namespace: u.apiManager.Namespace}, existing)
	if err != nil {
		return reconcile.Result{}, err
	}

	changed, err := u.ensureDeploymentConfigImageChangeTrigger(desired, existing)
	if err != nil {
		return reconcile.Result{}, err
	}
	if changed {
		return reconcile.Result{Requeue: true}, u.UpdateResource(existing)
	}

	return reconcile.Result{}, nil
}

func (u *UpgradeApiManager) ensureDeploymentConfigImageChangeTrigger(desired, existing *appsv1.DeploymentConfig) (bool, error) {
	desiredDeploymentTriggerImageChangePos, err := u.findDeploymentTriggerOnImageChange(desired.Spec.Triggers)
	if err != nil {
		return false, fmt.Errorf("unexpected: '%s' in DeploymentConfig '%s'", err, desired.Name)

	}
	existingDeploymentTriggerImageChangePos, err := u.findDeploymentTriggerOnImageChange(existing.Spec.Triggers)
	if err != nil {
		return false, fmt.Errorf("unexpected: '%s' in DeploymentConfig '%s'", err, existing.Name)
	}

	desiredDeploymentTriggerImageChangeParams := desired.Spec.Triggers[desiredDeploymentTriggerImageChangePos].ImageChangeParams
	existingDeploymentTriggerImageChangeParams := existing.Spec.Triggers[existingDeploymentTriggerImageChangePos].ImageChangeParams

	if !reflect.DeepEqual(existingDeploymentTriggerImageChangeParams.From.Name, desiredDeploymentTriggerImageChangeParams.From.Name) {
		diff := cmp.Diff(existingDeploymentTriggerImageChangeParams.From.Name, desiredDeploymentTriggerImageChangeParams.From.Name)
		u.Logger().V(1).Info(fmt.Sprintf("%s ImageStream tag name in imageChangeParams trigger changed: %s", desired.Name, diff))
		existingDeploymentTriggerImageChangeParams.From.Name = desiredDeploymentTriggerImageChangeParams.From.Name
		return true, nil
	}

	return false, nil
}

func (u *UpgradeApiManager) upgradeAMPImageStreams() (reconcile.Result, error) {
	// implement upgrade procedure by reconcile procedure
	reconciler := NewAMPImagesReconciler(NewBaseAPIManagerLogicReconciler(u.BaseReconciler, u.apiManager))
	return reconciler.Reconcile()
}

func (u *UpgradeApiManager) upgradeBackendRedisImageStream() (reconcile.Result, error) {
	redis, err := Redis(u.apiManager, u.Client())
	if err != nil {
		return reconcile.Result{}, err
	}

	reconciler := NewBaseAPIManagerLogicReconciler(u.BaseReconciler, u.apiManager)
	return reconcile.Result{}, reconciler.ReconcileImagestream(redis.BackendImageStream(), reconcilers.GenericImageStreamMutator)
}

func (u *UpgradeApiManager) upgradeSystemRedisImageStream() (reconcile.Result, error) {
	redis, err := Redis(u.apiManager, u.Client())
	if err != nil {
		return reconcile.Result{}, err
	}

	reconciler := NewBaseAPIManagerLogicReconciler(u.BaseReconciler, u.apiManager)
	return reconcile.Result{}, reconciler.ReconcileImagestream(redis.SystemImageStream(), reconcilers.GenericImageStreamMutator)
}

func (u *UpgradeApiManager) upgradeSystemDatabaseImageStream() (reconcile.Result, error) {
	if u.apiManager.Spec.System.DatabaseSpec != nil && u.apiManager.Spec.System.DatabaseSpec.PostgreSQL != nil {
		return u.upgradeSystemPostgreSQLImageStream()
	}

	// default is MySQL
	return u.upgradeSystemMySQLImageStream()
}

func (u *UpgradeApiManager) upgradeSystemMySQLImageStream() (reconcile.Result, error) {
	// implement upgrade procedure by reconcile procedure
	reconciler := NewSystemMySQLImageReconciler(NewBaseAPIManagerLogicReconciler(u.BaseReconciler, u.apiManager))
	return reconciler.Reconcile()
}

func (u *UpgradeApiManager) upgradeSystemPostgreSQLImageStream() (reconcile.Result, error) {
	// implement upgrade procedure by reconcile procedure
	reconciler := NewSystemPostgreSQLImageReconciler(NewBaseAPIManagerLogicReconciler(u.BaseReconciler, u.apiManager))
	return reconciler.Reconcile()
}

func (u *UpgradeApiManager) deleteMessageBusConfigurations() (reconcile.Result, error) {
	res, err := u.deleteSystemAppMessageBusConfigurations()
	if res.Requeue || err != nil {
		return res, err
	}
	res, err = u.deleteSystemSidekiqMessageBusConfigurations()
	if res.Requeue || err != nil {
		return res, err
	}
	res, err = u.deleteSystemSphinxMessageBusConfigurations()
	if res.Requeue || err != nil {
		return res, err
	}

	if !u.apiManager.IsExternal(appsv1alpha1.SystemRedis) {
		res, err = u.deleteSystemRedisMessageBusSecretAttributes()
		if res.Requeue || err != nil {
			return res, err
		}
	}

	return reconcile.Result{}, nil
}

func (u *UpgradeApiManager) messageBusEnvVarNames() []string {
	return []string{
		"MESSAGE_BUS_REDIS_URL",
		"MESSAGE_BUS_REDIS_NAMESPACE",
		"MESSAGE_BUS_REDIS_SENTINEL_HOSTS",
		"MESSAGE_BUS_REDIS_SENTINEL_ROLE",
	}
}

func (u *UpgradeApiManager) deleteSystemAppMessageBusConfigurations() (reconcile.Result, error) {
	system, err := System(u.apiManager, u.Client())
	if err != nil {
		return reconcile.Result{}, err
	}
	desired := system.AppDeploymentConfig()

	res, err := u.deleteDeploymentConfigEnvVars(desired, u.messageBusEnvVarNames())
	return res, err
}

func (u *UpgradeApiManager) deleteSystemSidekiqMessageBusConfigurations() (reconcile.Result, error) {
	system, err := System(u.apiManager, u.Client())
	if err != nil {
		return reconcile.Result{}, err
	}
	desired := system.SidekiqDeploymentConfig()

	res, err := u.deleteDeploymentConfigEnvVars(desired, u.messageBusEnvVarNames())
	return res, err
}

func (u *UpgradeApiManager) deleteSystemSphinxMessageBusConfigurations() (reconcile.Result, error) {
	system, err := System(u.apiManager, u.Client())
	if err != nil {
		return reconcile.Result{}, err
	}
	desired := system.SphinxDeploymentConfig()

	res, err := u.deleteDeploymentConfigEnvVars(desired, u.messageBusEnvVarNames())
	return res, err
}

func (u *UpgradeApiManager) deleteSystemRedisMessageBusSecretAttributes() (reconcile.Result, error) {
	redis, err := Redis(u.apiManager, u.Client())
	if err != nil {
		return reconcile.Result{}, err
	}

	desired := redis.SystemRedisSecret()

	// component.SystemSecretSystemRedisSecretName
	existing := &v1.Secret{}
	err = u.Client().Get(context.TODO(), types.NamespacedName{Name: desired.Name, Namespace: u.apiManager.Namespace}, existing)
	if err != nil {
		return reconcile.Result{}, err
	}

	systemRedisSecretMessageBusAttributeNames := []string{
		"MESSAGE_BUS_URL",
		"MESSAGE_BUS_NAMESPACE",
		"MESSAGE_BUS_SENTINEL_HOSTS",
		"MESSAGE_BUS_SENTINEL_ROLE",
	}

	update := false

	for _, secretAttr := range systemRedisSecretMessageBusAttributeNames {
		if _, ok := existing.Data[secretAttr]; ok {
			update = true
			delete(existing.Data, secretAttr)
		}
	}

	if update {
		err = u.UpdateResource(existing)
		if err != nil {
			return reconcile.Result{}, err
		}
	}

	return reconcile.Result{}, nil
}

// deleteDeploymentConfigEnvVars deletes the environment variable names specified in
// envVarNames from the given `desired` DeploymentConfig name. It deletes the
// environment variables from all of its containers, initContainers, pre-hook
// pods, post-hook pods and mid-hook pods.
func (u *UpgradeApiManager) deleteDeploymentConfigEnvVars(desired *appsv1.DeploymentConfig, envVarNames []string) (reconcile.Result, error) {
	existing := &appsv1.DeploymentConfig{}
	err := u.Client().Get(context.TODO(), types.NamespacedName{Name: desired.Name, Namespace: u.apiManager.Namespace}, existing)
	if err != nil {
		return reconcile.Result{}, err
	}

	update := false

	// containers
	for containerIdx := range existing.Spec.Template.Spec.Containers {
		container := &existing.Spec.Template.Spec.Containers[containerIdx]
		for _, envVarName := range envVarNames {
			if envVarIdx := helper.FindEnvVar(container.Env, envVarName); envVarIdx >= 0 {
				// remove index
				container.Env = append(container.Env[:envVarIdx], container.Env[envVarIdx+1:]...)
				update = true
			}
		}
	}

	// initContainers
	for initContainerIdx := range existing.Spec.Template.Spec.InitContainers {
		initContainer := &existing.Spec.Template.Spec.InitContainers[initContainerIdx]
		for _, envVarName := range envVarNames {
			if envVarIdx := helper.FindEnvVar(initContainer.Env, envVarName); envVarIdx >= 0 {
				// remove index
				initContainer.Env = append(initContainer.Env[:envVarIdx], initContainer.Env[envVarIdx+1:]...)
				update = true
			}
		}
	}

	// pre-hook pod
	var preHookPod *appsv1.ExecNewPodHook
	if existing.Spec.Strategy.RollingParams != nil && existing.Spec.Strategy.RollingParams.Pre != nil && existing.Spec.Strategy.RollingParams.Pre.ExecNewPod != nil {
		preHookPod = existing.Spec.Strategy.RollingParams.Pre.ExecNewPod
	}
	if existing.Spec.Strategy.RecreateParams != nil && existing.Spec.Strategy.RecreateParams.Pre != nil && existing.Spec.Strategy.RecreateParams.Pre.ExecNewPod != nil {
		preHookPod = existing.Spec.Strategy.RecreateParams.Pre.ExecNewPod
	}

	if preHookPod != nil {
		for _, envVarName := range envVarNames {
			if envVarIdx := helper.FindEnvVar(preHookPod.Env, envVarName); envVarIdx >= 0 {
				// remove index
				preHookPod.Env = append(preHookPod.Env[:envVarIdx], preHookPod.Env[envVarIdx+1:]...)
				update = true
			}
		}
	}

	// post-hook pod
	var postHookPod *appsv1.ExecNewPodHook
	if existing.Spec.Strategy.RollingParams != nil && existing.Spec.Strategy.RollingParams.Post != nil && existing.Spec.Strategy.RollingParams.Post.ExecNewPod != nil {
		postHookPod = existing.Spec.Strategy.RollingParams.Post.ExecNewPod
	}
	if existing.Spec.Strategy.RecreateParams != nil && existing.Spec.Strategy.RecreateParams.Post != nil && existing.Spec.Strategy.RecreateParams.Post.ExecNewPod != nil {
		postHookPod = existing.Spec.Strategy.RecreateParams.Post.ExecNewPod
	}

	if postHookPod != nil {
		for _, envVarName := range envVarNames {
			if envVarIdx := helper.FindEnvVar(postHookPod.Env, envVarName); envVarIdx >= 0 {
				// remove index
				postHookPod.Env = append(postHookPod.Env[:envVarIdx], postHookPod.Env[envVarIdx+1:]...)
				update = true
			}
		}
	}

	// mid-hook pod
	var midHookPod *appsv1.ExecNewPodHook
	if existing.Spec.Strategy.RecreateParams != nil && existing.Spec.Strategy.RecreateParams.Mid != nil && existing.Spec.Strategy.RecreateParams.Mid.ExecNewPod != nil {
		midHookPod = existing.Spec.Strategy.RecreateParams.Mid.ExecNewPod
	}

	if midHookPod != nil {
		for _, envVarName := range envVarNames {
			if envVarIdx := helper.FindEnvVar(midHookPod.Env, envVarName); envVarIdx >= 0 {
				// remove index
				midHookPod.Env = append(midHookPod.Env[:envVarIdx], midHookPod.Env[envVarIdx+1:]...)
				update = true
			}
		}
	}

	if update {
		err = u.UpdateResource(existing)
		if err != nil {
			return reconcile.Result{}, err
		}
	}

	return reconcile.Result{}, nil
}

func (u *UpgradeApiManager) findDeploymentTriggerOnImageChange(triggerPolicies []appsv1.DeploymentTriggerPolicy) (int, error) {
	result := -1
	for i := range triggerPolicies {
		if triggerPolicies[i].Type == appsv1.DeploymentTriggerOnImageChange {
			if result != -1 {
				return -1, fmt.Errorf("found more than one imageChangeParams Deployment trigger policy")
			}
			result = i
		}
	}

	if result == -1 {
		return -1, fmt.Errorf("no imageChangeParams deployment trigger policy found")
	}

	return result, nil
}

func (u *UpgradeApiManager) upgradePodTemplateLabels() (reconcile.Result, error) {
	apicast, err := Apicast(u.apiManager, u.Client())
	if err != nil {
		return reconcile.Result{}, err
	}
	backend, err := Backend(u.apiManager, u.Client())
	if err != nil {
		return reconcile.Result{}, err
	}
	zync, err := Zync(u.apiManager, u.Client())
	if err != nil {
		return reconcile.Result{}, err
	}
	memcached, err := Memcached(u.apiManager)
	if err != nil {
		return reconcile.Result{}, err
	}
	system, err := System(u.apiManager, u.Client())
	if err != nil {
		return reconcile.Result{}, err
	}

	deploymentConfigs := []*appsv1.DeploymentConfig{
		apicast.StagingDeploymentConfig(),
		apicast.ProductionDeploymentConfig(),
		backend.ListenerDeploymentConfig(),
		backend.WorkerDeploymentConfig(),
		backend.CronDeploymentConfig(),
		zync.DeploymentConfig(),
		zync.QueDeploymentConfig(),
		memcached.DeploymentConfig(),
		system.AppDeploymentConfig(),
		system.SidekiqDeploymentConfig(),
		system.SphinxDeploymentConfig(),
	}

	if !u.apiManager.IsExternal(appsv1alpha1.SystemRedis) || !u.apiManager.IsExternal(appsv1alpha1.BackendRedis) {
		redis, err := Redis(u.apiManager, u.Client())
		if err != nil {
			return reconcile.Result{}, err
		}
		if !u.apiManager.IsExternal(appsv1alpha1.SystemRedis) {
			deploymentConfigs = append(deploymentConfigs, redis.SystemDeploymentConfig())
		}
		if !u.apiManager.IsExternal(appsv1alpha1.BackendRedis) {
			deploymentConfigs = append(deploymentConfigs, redis.BackendDeploymentConfig())
		}
	}

	if u.apiManager.IsSystemPostgreSQLEnabled() {
		systemPostgreSQL, err := SystemPostgreSQL(u.apiManager, u.Client())
		if err != nil {
			return reconcile.Result{}, err
		}
		deploymentConfigs = append(deploymentConfigs, systemPostgreSQL.DeploymentConfig())
	}

	if u.apiManager.IsSystemMysqlEnabled() {
		systemMySQL, err := SystemMySQL(u.apiManager, u.Client())
		if err != nil {
			return reconcile.Result{}, err
		}
		deploymentConfigs = append(deploymentConfigs, systemMySQL.DeploymentConfig())
	}

	if !u.apiManager.IsExternal(v1alpha1.ZyncDatabase) {
		deploymentConfigs = append(deploymentConfigs, zync.DatabaseDeploymentConfig())
	}

	updated := false
	for _, desired := range deploymentConfigs {
		updatedTmp, err := u.ensurePodTemplateLabels(desired)
		if err != nil {
			return reconcile.Result{}, err
		}
		updated = updated || updatedTmp
	}

	return reconcile.Result{Requeue: updated}, nil
}

func (u *UpgradeApiManager) ensurePodTemplateLabels(desired *appsv1.DeploymentConfig) (bool, error) {
	u.Logger().V(1).Info(fmt.Sprintf("ensurePodTemplateLabels object %s", common.ObjectInfo(desired)))
	existing := &appsv1.DeploymentConfig{}
	err := u.Client().Get(context.TODO(), types.NamespacedName{Name: desired.Name, Namespace: u.apiManager.Namespace}, existing)
	if err != nil {
		return false, err
	}

	updated := false

	diff := cmp.Diff(existing.Spec.Template.Labels, desired.Spec.Template.Labels)
	helper.MergeMapStringString(&updated, &existing.Spec.Template.Labels, desired.Spec.Template.Labels)

	// Remove old metering labels
	oldLabelKeys := []string{"com.redhat.product-name", "com.redhat.component-type",
		"com.redhat.product-version", "com.redhat.component-version",
		"com.redhat.component-name"}

	for _, key := range oldLabelKeys {
		if _, ok := existing.Spec.Template.Labels[key]; ok {
			delete(existing.Spec.Template.Labels, key)
			updated = true
		}
	}

	if updated {
		u.Logger().V(1).Info(fmt.Sprintf("DC %s template lables changed: %s", desired.Name, diff))
		err = u.UpdateResource(existing)
		if err != nil {
			return false, err
		}
	}

	return updated, nil
}

func (u *UpgradeApiManager) upgradeMysqlConfigmap() (reconcile.Result, error) {
	if !u.apiManager.IsSystemMysqlEnabled() {
		return reconcile.Result{}, nil
	}

	mysqlExtraConfigMap := &v1.ConfigMap{}
	err := u.Client().Get(context.TODO(), types.NamespacedName{Name: "mysql-extra-conf", Namespace: u.apiManager.Namespace}, mysqlExtraConfigMap)
	if err != nil {
		return reconcile.Result{}, err
	}

	if _, ok := mysqlExtraConfigMap.Data["mysql-default-authentication-plugin.cnf"]; !ok {
		mysqlExtraConfigMap.Data["mysql-default-authentication-plugin.cnf"] = `[mysqld]
default_authentication_plugin=mysql_native_password
`
		err = u.UpdateResource(mysqlExtraConfigMap)
		if err != nil {
			return reconcile.Result{}, err
		}
	}

	return reconcile.Result{}, nil
}

func (u *UpgradeApiManager) Logger() logr.Logger {
	return u.logger
}

// Helper components to declaratively build upgrade functions depending on whether
// certain dependencies are internal or external

type upgradeDependencyFn func(*UpgradeApiManager) (reconcile.Result, error)

func sequentially(fns ...upgradeDependencyFn) upgradeDependencyFn {
	return func(u *UpgradeApiManager) (reconcile.Result, error) {
		for _, fn := range fns {
			res, err := fn(u)
			if res.Requeue || err != nil {
				return res, err
			}
		}

		return reconcile.Result{}, nil
	}
}

func andThen(first, then upgradeDependencyFn) upgradeDependencyFn {
	return func(u *UpgradeApiManager) (reconcile.Result, error) {
		res, err := first(u)
		if res.Requeue || err != nil {
			return res, err
		}

		return then(u)
	}
}

func when(condition func(*UpgradeApiManager) bool, fn upgradeDependencyFn) upgradeDependencyFn {
	return func(u *UpgradeApiManager) (reconcile.Result, error) {
		if condition(u) {
			return fn(u)
		}

		return reconcile.Result{}, nil
	}
}

func notExternal(selector func(*v1alpha1.ExternalComponentsSpec) bool) func(*UpgradeApiManager) bool {
	return func(u *UpgradeApiManager) bool {
		return !u.apiManager.IsExternal(selector)
	}
}
