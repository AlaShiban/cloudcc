package aws

import (
	"github.com/cloudcompiler/cloudcc/internal/sanitize"
)

// Environment variable names for each capability's runtime binding.
//
// These spellings are the contract between the generated Pulumi project, which
// sets them, and the injected _cloudcc_runtime shims, which read them. The shims
// build the same names in Python; a parity test pins the two together.
func EnvKVTable(id string) string       { return "CLOUDCC_KV_" + sanitize.EnvVar(id) + "_TABLE" }
func EnvFSBucket(id string) string      { return "CLOUDCC_FS_" + sanitize.EnvVar(id) + "_BUCKET" }
func EnvSecretARN(id string) string     { return "CLOUDCC_SECRET_" + sanitize.EnvVar(id) + "_ARN" }
func EnvORMURL(id string) string        { return "CLOUDCC_ORM_" + sanitize.EnvVar(id) + "_URL" }
func EnvORMSecretARN(id string) string  { return "CLOUDCC_ORM_" + sanitize.EnvVar(id) + "_SECRET_ARN" }
func EnvRedisEndpoint(id string) string { return "CLOUDCC_REDIS_" + sanitize.EnvVar(id) + "_ENDPOINT" }
func EnvRedisPort(id string) string     { return "CLOUDCC_REDIS_" + sanitize.EnvVar(id) + "_PORT" }
func EnvRedisTLS(id string) string      { return "CLOUDCC_REDIS_" + sanitize.EnvVar(id) + "_TLS" }
func EnvTopicARN(id string) string      { return "CLOUDCC_TOPIC_" + sanitize.EnvVar(id) + "_ARN" }
func EnvConfig(id string) string        { return "CLOUDCC_CONFIG_" + sanitize.EnvVar(id) }
func EnvGatewayURL(id string) string    { return "CLOUDCC_GATEWAY_" + sanitize.EnvVar(id) + "_URL" }
func EnvStaticBucket(id string) string  { return "CLOUDCC_STATIC_" + sanitize.EnvVar(id) + "_BUCKET" }
func EnvStaticURL(id string) string     { return "CLOUDCC_STATIC_" + sanitize.EnvVar(id) + "_URL" }

func EnvECRRepo(id string) string { return "CLOUDCC_ECR_" + sanitize.EnvVar(id) + "_URL" }

// EnvEndpointOverride is honoured by every shim so a compiled application can
// be pointed at an AWS emulator with no code change (D15).
const EnvEndpointOverride = "CLOUDCC_AWS_ENDPOINT_URL"

// Concrete resource kinds. A resource Key's Kind is one of these, which is
// what keeps intent nodes and resource nodes from ever colliding.
const (
	KindLambda         = "aws.lambda"
	KindLambdaRole     = "aws.iam.role"
	KindLambdaPolicy   = "aws.iam.policy"
	KindLogGroup       = "aws.cloudwatch.loggroup"
	KindAPIGatewayV2   = "aws.apigatewayv2"
	KindAPIIntegration = "aws.apigatewayv2.integration"
	KindAPIRoute       = "aws.apigatewayv2.route"
	KindAPIStage       = "aws.apigatewayv2.stage"
	KindLambdaPerm     = "aws.lambda.permission"
	KindDynamoTable    = "aws.dynamodb"
	KindS3Bucket       = "aws.s3"
	KindS3Object       = "aws.s3.object"
	KindS3Website      = "aws.s3.website"
	KindSecret         = "aws.secretsmanager"
	KindSNSTopic       = "aws.sns"
	KindSNSSub         = "aws.sns.subscription"
	KindRDS            = "aws.rds"
	KindElastiCache    = "aws.elasticache"
	KindMemoryDB       = "aws.memorydb"
	KindECSCluster     = "aws.ecs.cluster"
	KindECSService     = "aws.ecs.service"
	KindECSTask        = "aws.ecs.taskdefinition"
	KindECRRepo        = "aws.ecr"
	KindALB            = "aws.alb"
	KindVPC            = "aws.vpc"
	KindSubnet         = "aws.vpc.subnet"
	KindSecurityGroup  = "aws.vpc.securitygroup"
	KindInternetGW     = "aws.vpc.gateway"
	KindRouteTable     = "aws.vpc.routetable"
	KindRouteAssoc     = "aws.vpc.routeassoc"
	KindAvailZones     = "aws.availabilityzones"
	KindSubnetGroup    = "aws.subnetgroup"
	KindALBTargetGroup = "aws.alb.targetgroup"
	KindALBListener    = "aws.alb.listener"
	KindECSExecRole    = "aws.ecs.execrole"
	KindECSTaskRole    = "aws.ecs.taskrole"
	KindECSTaskPolicy  = "aws.ecs.taskpolicy"
)
