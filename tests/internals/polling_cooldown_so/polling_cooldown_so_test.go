//go:build e2e
// +build e2e

package polling_cooldown_so_test

import (
	"fmt"
	"testing"

	"github.com/joho/godotenv"
	"github.com/stretchr/testify/assert"
	"k8s.io/client-go/kubernetes"

	. "github.com/kedacore/keda/v2/tests/helper"
)

const (
	testName = "polling-cooldown-so-test"
)

// Load environment variables from .env file
var _ = godotenv.Load("../../.env")

var (
	namespace                   = fmt.Sprintf("%s-ns", testName)
	deploymentName              = fmt.Sprintf("%s-deployment", testName)
	metricsServerDeploymentName = fmt.Sprintf("%s-metrics-server", testName)
	serviceName                 = fmt.Sprintf("%s-service", testName)
	triggerAuthName             = fmt.Sprintf("%s-ta", testName)
	scaledObjectName            = fmt.Sprintf("%s-so", testName)
	secretName                  = fmt.Sprintf("%s-secret", testName)
	metricsServerEndpoint       = fmt.Sprintf("http://%s.%s.svc.cluster.local:8080/api/value", serviceName, namespace)
	minReplicas                 = 0
	maxReplicas                 = 5
	pollingInterval             = 0
	cooldownPeriod              = 0
)

type templateData struct {
	TestNamespace               string
	DeploymentName              string
	ScaledObject                string
	TriggerAuthName             string
	SecretName                  string
	ServiceName                 string
	MetricsServerDeploymentName string
	MetricsServerEndpoint       string
	MinReplicas                 string
	MaxReplicas                 string
	MetricValue                 int
	PollingInterval             int
	CooldownPeriod              int
}

const (
	secretTemplate = `apiVersion: v1
kind: Secret
metadata:
  name: {{.SecretName}}
  namespace: {{.TestNamespace}}
data:
  AUTH_PASSWORD: U0VDUkVUCg==
  AUTH_USERNAME: VVNFUgo=
`

	triggerAuthenticationTemplate = `apiVersion: keda.sh/v1alpha1
kind: TriggerAuthentication
metadata:
  name: {{.TriggerAuthName}}
  namespace: {{.TestNamespace}}
spec:
  secretTargetRef:
    - parameter: username
      name: {{.SecretName}}
      key: AUTH_USERNAME
    - parameter: password
      name: {{.SecretName}}
      key: AUTH_PASSWORD
`

	deploymentTemplate = `
apiVersion: apps/v1
kind: Deployment
metadata:
  labels:
    app: {{.DeploymentName}}
  name: {{.DeploymentName}}
  namespace: {{.TestNamespace}}
spec:
  selector:
    matchLabels:
      app: {{.DeploymentName}}
  replicas: 0
  template:
    metadata:
      labels:
        app: {{.DeploymentName}}
    spec:
      containers:
      - name: nginx
        image: nginxinc/nginx-unprivileged
        ports:
        - containerPort: 80
`

	metricsServerDeploymentTemplate = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{.MetricsServerDeploymentName}}
  namespace: {{.TestNamespace}}
  labels:
    app: {{.MetricsServerDeploymentName}}
spec:
  replicas: 1
  selector:
    matchLabels:
      app: {{.MetricsServerDeploymentName}}
  template:
    metadata:
      labels:
        app: {{.MetricsServerDeploymentName}}
    spec:
      containers:
      - name: metrics
        image: ghcr.io/kedacore/tests-metrics-api
        ports:
        - containerPort: 8080
        envFrom:
        - secretRef:
            name: {{.SecretName}}
        imagePullPolicy: Always
`

	scaledObjectTemplate = `
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata:
  name: {{.ScaledObject}}
  namespace: {{.TestNamespace}}
  labels:
    app: {{.DeploymentName}}
spec:
  scaleTargetRef:
    name: {{.DeploymentName}}
  pollingInterval: {{.PollingInterval}}
  cooldownPeriod: {{.CooldownPeriod}}
  minReplicaCount: {{.MinReplicas}}
  maxReplicaCount: {{.MaxReplicas}}
  triggers:
  - type: metrics-api
    metadata:
      targetValue: "1"
      url: "{{.MetricsServerEndpoint}}"
      valueLocation: 'value'
      method: "query"
    authenticationRef:
      name: {{.TriggerAuthName}}
`

	updateMetricsTemplate = `
apiVersion: batch/v1
kind: Job
metadata:
  generateName: update-ms-value-
  namespace: {{.TestNamespace}}
spec:
  template:
    spec:
      containers:
      - name: job-curl
        image: curlimages/curl
        imagePullPolicy: Always
        command: ["curl", "-X", "POST", "{{.MetricsServerEndpoint}}/{{.MetricValue}}"]
      restartPolicy: Never
`

	serviceTemplate = `
apiVersion: v1
kind: Service
metadata:
  name: {{.ServiceName}}
  namespace: {{.TestNamespace}}
spec:
  selector:
    app: {{.MetricsServerDeploymentName}}
  ports:
  - port: 8080
    targetPort: 8080
`
)

// use KubectlCreateWithTemplate() function because applying yaml template
// for update-metrics relies on the previous job to be deleted
// (job is immutable), but in this case the test runs too fast at some
// points that job is not finished & deleted in time

func TestPollingInterval(t *testing.T) {
	// setup
	t.Log("--- setting up ---")
	kc := GetKubernetesClient(t)
	data, templates := getTemplateData()
	CreateKubernetesResources(t, kc, namespace, data, templates)

	assert.True(t, WaitForDeploymentReplicaReadyCount(t, kc, deploymentName, namespace, minReplicas, 180, 3),
		"replica count should be %d after 3 minutes", minReplicas)

	testPollingIntervalUp(t, kc, data)
	testPollingIntervalDown(t, kc, data)
	testCooldownPeriod(t, kc, data)

	DeleteKubernetesResources(t, kc, namespace, data, templates)
}

func testPollingIntervalUp(t *testing.T, kc *kubernetes.Clientset, data templateData) {
	t.Log("--- test Polling Interval up ---")

	data.MetricValue = 0
	KubectlCreateWithTemplate(t, data, "updateMetricsTemplate", updateMetricsTemplate)

	assert.True(t, WaitForDeploymentReplicaReadyCount(t, kc, deploymentName, namespace, 0, 180, 3),
		"replica count should be %d after 3 minutes", 0)

	// wait for atleast 60+5 seconds before getting new metric
	data.PollingInterval = 60 + 5 // 5 seconds as a reserve
	KubectlApplyWithTemplate(t, data, "scaledObjectTemplate", scaledObjectTemplate)

	data.MetricValue = 1
	KubectlCreateWithTemplate(t, data, "updateMetricsTemplate", updateMetricsTemplate)

	AssertReplicaCountNotChangeDuringTimePeriod(t, kc, deploymentName, namespace, 0, 60)

	assert.True(t, WaitForDeploymentReplicaReadyCount(t, kc, deploymentName, namespace, 1, 180, 3),
		"replica count should be %d after 3 minutes", 1)
}

func testPollingIntervalDown(t *testing.T, kc *kubernetes.Clientset, data templateData) {
	t.Log("--- test Polling Interval down ---")

	data.CooldownPeriod = 0 // remove cooldownPeriod to test PI
	data.PollingInterval = 1
	KubectlApplyWithTemplate(t, data, "scaledObjectTemplate", scaledObjectTemplate)

	data.MetricValue = 1
	KubectlCreateWithTemplate(t, data, "updateMetricsTemplate", updateMetricsTemplate)

	assert.True(t, WaitForDeploymentReplicaReadyCount(t, kc, deploymentName, namespace, 1, 180, 3),
		"replica count should be %d after 3 minutes", 1)

	// wait for atleast 60+5 seconds before getting new metric
	data.PollingInterval = 60 + 5 // 5 seconds as a reserve

	KubectlApplyWithTemplate(t, data, "scaledObjectTemplate", scaledObjectTemplate)

	data.MetricValue = 0 // go to minReplicas
	KubectlCreateWithTemplate(t, data, "updateMetricsTemplate", updateMetricsTemplate)

	AssertReplicaCountNotChangeDuringTimePeriod(t, kc, deploymentName, namespace, 1, 60)

	assert.True(t, WaitForDeploymentReplicaReadyCount(t, kc, deploymentName, namespace, 0, 180, 3),
		"replica count should be %d after 3 minutes", 0)
}

func testCooldownPeriod(t *testing.T, kc *kubernetes.Clientset, data templateData) {
	t.Log("--- test Cooldown Period ---")

	data.PollingInterval = 0     // remove polling interval to test CP
	data.CooldownPeriod = 60 + 5 // 5 seconds as a reserve
	KubectlApplyWithTemplate(t, data, "scaledObjectTemplate", scaledObjectTemplate)

	data.MetricValue = 1
	KubectlCreateWithTemplate(t, data, "updateMetricsTemplate", updateMetricsTemplate)

	assert.True(t, WaitForDeploymentReplicaReadyCount(t, kc, deploymentName, namespace, 1, 180, 3),
		"replica count should be %d after 3 minutes", 1)

	data.MetricValue = 0
	KubectlCreateWithTemplate(t, data, "updateMetricsTemplate", updateMetricsTemplate)

	AssertReplicaCountNotChangeDuringTimePeriod(t, kc, deploymentName, namespace, 1, 60)

	assert.True(t, WaitForDeploymentReplicaReadyCount(t, kc, deploymentName, namespace, 0, 180, 3),
		"replica count should be %d after 3 minutes", 0)
}

func getTemplateData() (templateData, []Template) {
	return templateData{
			TestNamespace:               namespace,
			DeploymentName:              deploymentName,
			MetricsServerDeploymentName: metricsServerDeploymentName,
			ServiceName:                 serviceName,
			TriggerAuthName:             triggerAuthName,
			ScaledObject:                scaledObjectName,
			SecretName:                  secretName,
			MetricsServerEndpoint:       metricsServerEndpoint,
			MinReplicas:                 fmt.Sprintf("%v", minReplicas),
			MaxReplicas:                 fmt.Sprintf("%v", maxReplicas),
			MetricValue:                 0,
			PollingInterval:             pollingInterval,
			CooldownPeriod:              cooldownPeriod,
		}, []Template{
			{Name: "secretTemplate", Config: secretTemplate},
			{Name: "metricsServerDeploymentTemplate", Config: metricsServerDeploymentTemplate},
			{Name: "serviceTemplate", Config: serviceTemplate},
			{Name: "triggerAuthenticationTemplate", Config: triggerAuthenticationTemplate},
			{Name: "deploymentTemplate", Config: deploymentTemplate},
			{Name: "scaledObjectTemplate", Config: scaledObjectTemplate},
		}
}