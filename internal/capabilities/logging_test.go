package capabilities_test

import (
	"strings"
	"testing"

	"github.com/cloudcompiler/cloudcc/internal/config"
	"github.com/cloudcompiler/cloudcc/internal/provider/aws"
)

// Where logs go is the one capability declared only in configuration: a program
// does not choose its log destination, an operator does, and the call sites are
// identical either way.
//
// Which means nothing in the source program would ever mention it -- so the
// usual validation walk over intents cannot see it, and an unrecognised
// destination would be silently dropped. These tests are what stop that.

func TestCloudWatchIsTheDefaultDestination(t *testing.T) {
	app := config.New()
	got := app.LogDestination()
	if got.Type != "cloudwatch" {
		t.Errorf("default destination = %q, want cloudwatch", got.Type)
	}
	if got.RetentionDays != config.DefaultLogRetentionDays {
		t.Errorf("default retention = %d, want %d",
			got.RetentionDays, config.DefaultLogRetentionDays)
	}
}

func TestTheConfiguredDestinationWins(t *testing.T) {
	app := config.New()
	app.Logging = config.ResourceConfig{Type: "datadog", RetentionDays: 30}

	got := app.LogDestination()
	if got.Type != "datadog" || got.RetentionDays != 30 {
		t.Errorf("configured destination = %+v", got)
	}
}

// A vendor destination is recognised and refused, not ignored. The distinction
// matters: an unknown key that is dropped leaves an application looking
// configured when it is not, and the first anyone hears of it is that the logs
// never arrived.
func TestVendorDestinationsAreRefusedRatherThanIgnored(t *testing.T) {
	for _, vendor := range []string{"datadog", "honeycomb"} {
		if got := aws.Support(config.KindLogging, vendor); got != aws.NotYetSupported {
			t.Errorf("%s support = %v, want NotYetSupported: a destination that is "+
				"planned but unimplemented must be a clean error, never a silent "+
				"fallback to cloudwatch", vendor, got)
		}
	}
	if got := aws.Support(config.KindLogging, "cloudwatch"); got != aws.Supported {
		t.Errorf("cloudwatch support = %v, want Supported", got)
	}
	if got := aws.Support(config.KindLogging, "nonsense"); got != aws.Unknown {
		t.Errorf("an unrecognised destination should be Unknown, got %v", got)
	}
}

func TestOnlyCloudWatchIsImplemented(t *testing.T) {
	types := aws.SupportedTypes(config.KindLogging)
	if len(types) != 1 || types[0] != "cloudwatch" {
		t.Errorf("supported destinations = %v, want exactly [cloudwatch]", types)
	}
	// The vendors are still listed, which is what makes the error message
	// above possible.
	all := strings.Join(aws.AllTypes(config.KindLogging), ",")
	for _, vendor := range []string{"datadog", "honeycomb"} {
		if !strings.Contains(all, vendor) {
			t.Errorf("%s should be a known destination so choosing it is a clear "+
				"error; known are %s", vendor, all)
		}
	}
}
