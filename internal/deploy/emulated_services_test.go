package deploy

import (
	"strings"
	"testing"

	"github.com/cloudcompiler/cloudcc/internal/iac/pulumi_ts"
)

// endpointService maps a Pulumi module name to the provider's endpoint key,
// where the two differ.
//
// Pulumi names a module after the SDK namespace and the endpoint after the
// service's own API, and they part company in a handful of places: one module
// serves both kinds of load balancer, and a data source is not a service at
// all. Everything absent from this table maps to itself.
var endpointService = map[string]string{
	// aws:lb/loadBalancer:LoadBalancer is configured under elbv2, which is the
	// API that serves both application and network balancers.
	"lb": "elbv2",
	// A data source, not a resource type, and it reads the EC2 API.
	"getAvailabilityZones": "ec2",
	// The log group lives in the cloudwatch module and is served by the logs
	// endpoint; both spellings are in the list, because the provider has
	// historically accepted either.
	"cloudwatch": "logs",
}

// TestEveryEmittedServiceIsPointedAtTheEmulator is the check that would have
// caught SQS.
//
// A service the resolver can emit but this list omits is not a partial deploy:
// the provider sends that one call to the real AWS endpoint, which answers the
// emulator's throwaway credentials with InvalidClientTokenId and takes the whole
// stack down on its first resource. The failure has nothing to do with the
// resource that was added, which is why it is worth deriving from the registry
// rather than remembering.
func TestEveryEmittedServiceIsPointedAtTheEmulator(t *testing.T) {
	emulated := map[string]bool{}
	for _, s := range EmulatedServices {
		emulated[s] = true
	}

	for _, id := range pulumi_ts.TemplateIDs() {
		// Kubernetes resources are created through a provider built from the
		// cluster's own endpoint, not through the AWS one.
		if !strings.HasPrefix(id, "aws.") {
			continue
		}
		module := strings.Split(strings.TrimPrefix(id, "aws."), ".")[0]
		service := module
		if mapped, ok := endpointService[module]; ok {
			service = mapped
		}
		if !emulated[service] {
			t.Errorf("%s is emitted but %q is not in EmulatedServices, so deploying it "+
				"against an emulator sends that call to real AWS", id, service)
		}
	}
}
