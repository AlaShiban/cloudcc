package aws

import (
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/cloudcompiler/cloudcc/internal/config"
	"github.com/cloudcompiler/cloudcc/internal/ir"
)

func container(memory int, block map[string]any) config.ResourceConfig {
	cfg := config.ResourceConfig{Type: config.TypeContainer, Memory: memory}
	if block != nil {
		cfg.ProviderArgs = map[string]map[string]any{TaskDefinitionResourceKey: block}
	}
	return cfg
}

// The portable setting lands on the task definition, and the other half of the
// pair Fargate insists on is derived.
//
// Fargate will not infer either number, so a unit that says only how much
// memory it wants would otherwise have to name a CPU it has no opinion about.
// The smallest that fits is the cheapest legal answer, and it is written into
// the generated project where it can be read.
func TestPortableMemoryLandsOnTheTaskDefinition(t *testing.T) {
	for _, tc := range []struct{ memory, wantCPU int }{
		{512, 256},
		{2048, 256},
		{3072, 512},
		{4096, 512},
		{8192, 1024},
		{16384, 2048},
	} {
		cpu, memory, _, err := TaskDefinitionArgs("reporter", container(tc.memory, nil), 256, 512)
		if err != nil {
			t.Errorf("memory %d: %v", tc.memory, err)
			continue
		}
		if memory != strconv.Itoa(tc.memory) {
			t.Errorf("memory %d became %q", tc.memory, memory)
		}
		if cpu != strconv.Itoa(tc.wantCPU) {
			t.Errorf("memory %d derived cpu %q, want %d -- the smallest that holds it",
				tc.memory, cpu, tc.wantCPU)
		}
	}
}

// A portable setting is still checked against the host it lands on, and the
// accepted set is the intersection.
//
// `memory:` means the same thing on both compute types, which is what makes it
// portable. Which values are legal is not portable at all: a function takes
// anything from 128 MB upwards, and Fargate takes a short ladder. So the same
// line compiles as one type and cannot as the other, and saying so here beats
// AWS refusing the task definition at deploy time with "No Fargate
// configuration exists for given values" -- which names neither number.
func TestOnlyValuesTheHostAcceptsAreAccepted(t *testing.T) {
	for _, memory := range []int{128, 256, 384, 768, 1536, 2560, 5000, 9000} {
		if _, _, _, err := TaskDefinitionArgs("reporter", container(memory, nil), 256, 512); err == nil {
			t.Errorf("memory %d was accepted on a container; Fargate has no such size", memory)
		}

		// The same value on a function, to show the two really do differ rather
		// than the check being wrong in one place.
		fn := config.ResourceConfig{Type: config.TypeFunction, Memory: memory}
		if _, err := LambdaFunctionArgs("api", fn); err != nil && memory >= 128 && memory <= 10240 {
			t.Errorf("memory %d was refused on a function: %v", memory, err)
		}
	}
}

// A legal memory with a CPU that cannot hold it is its own mistake, and the
// message says what that CPU does take.
func TestACPUThatCannotHoldTheMemoryIsRefused(t *testing.T) {
	_, _, _, err := TaskDefinitionArgs("reporter",
		container(3072, map[string]any{"cpu": 256}), 256, 512)
	if err == nil {
		t.Fatal("256 CPU units with 3072 MB was accepted; Fargate has no such configuration")
	}
	for _, want := range []string{"256 CPU units", "3072 MB", "512, 1024 or 2048"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the diagnostic is missing %q:\n%s", want, err)
		}
	}

	// And the legal pairing is accepted.
	if _, _, _, err := TaskDefinitionArgs("reporter",
		container(3072, map[string]any{"cpu": 512}), 256, 512); err != nil {
		t.Errorf("512 CPU units with 3072 MB was refused: %v", err)
	}
}

// A setting that does not apply to this compute type is refused with the
// reason, rather than accepted and ignored.
func TestTimeoutDoesNotApplyToAContainer(t *testing.T) {
	cfg := container(2048, nil)
	cfg.Timeout = 60
	_, _, _, err := TaskDefinitionArgs("reporter", cfg, 256, 512)
	if err == nil {
		t.Fatal("`timeout:` was accepted on a container")
	}
	if !strings.Contains(err.Error(), "stays up") {
		t.Errorf("the refusal does not explain itself:\n%s", err)
	}
}

// The portable setting may not be restated in the provider block, on either
// compute type.
func TestMemoryMayNotBeWrittenInTheProviderBlock(t *testing.T) {
	_, _, _, err := TaskDefinitionArgs("reporter",
		container(0, map[string]any{"memory": 2048}), 256, 512)
	if err == nil {
		t.Fatal("aws.ecs.TaskDefinition.memory was accepted")
	}
	if !strings.Contains(err.Error(), "portable setting `memory:`") {
		t.Errorf("the diagnostic does not point at the portable layer:\n%s", err)
	}
}

// A provider block naming a resource this unit does not become is an error, so
// a block written for the wrong compute type cannot be silently ignored.
func TestAContainerRefusesTheFunctionsResourceBlock(t *testing.T) {
	cfg := config.ResourceConfig{
		Type:         config.TypeContainer,
		ProviderArgs: map[string]map[string]any{FunctionResourceKey: {"architectures": []any{"arm64"}}},
	}
	_, _, _, err := TaskDefinitionArgs("reporter", cfg, 256, 512)
	if err == nil {
		t.Fatal("aws.lambda.Function was accepted on a container unit")
	}
	if !strings.Contains(err.Error(), TaskDefinitionResourceKey) {
		t.Errorf("the diagnostic does not name what this unit is sized by:\n%s", err)
	}
}

// The size table has to be internally consistent, because every diagnostic
// above reads from it and a bad row would produce confident nonsense.
func TestTheFargateSizeTableIsConsistent(t *testing.T) {
	seenCPU := map[int]bool{}
	lastCPU := 0
	for _, size := range fargateSizes {
		if seenCPU[size.cpu] {
			t.Errorf("cpu %d appears twice", size.cpu)
		}
		seenCPU[size.cpu] = true
		if size.cpu <= lastCPU {
			t.Errorf("cpu %d follows %d; the table must ascend, because smallestCPUFor scans it in order",
				size.cpu, lastCPU)
		}
		lastCPU = size.cpu

		if len(size.memories) == 0 {
			t.Errorf("cpu %d accepts no memory at all", size.cpu)
			continue
		}
		last := 0
		for _, m := range size.memories {
			if m <= last {
				t.Errorf("cpu %d: memory %d follows %d; each row must ascend", size.cpu, m, last)
			}
			last = m
		}
		// Every row's smallest memory must be one the row above could not
		// hold more cheaply, or smallestCPUFor would never choose this row.
		if got := smallestCPUFor(size.memories[len(size.memories)-1]); got > size.cpu {
			t.Errorf("cpu %d's largest memory %d resolves to cpu %d",
				size.cpu, size.memories[len(size.memories)-1], got)
		}
	}
}

// A container unit says where it runs, and the two answers produce different
// resources from the same declaration.
//
// The point of `platform:` being its own axis is that everything else stays
// put: the unit is a container either way, `memory:` means the same thing, and
// the image is the same image. Only what runs it changes.
func TestThePlatformDecidesWhatRunsTheContainer(t *testing.T) {
	for _, tc := range []struct {
		platform string
		wants    []string
		absent   []string
	}{
		{config.PlatformServerless,
			[]string{"aws.ecs.TaskDefinition", "aws.ecs.Service"},
			[]string{"k8s.apps.v1.Deployment", "aws.eks.Cluster"}},
		{config.PlatformKubernetes,
			[]string{"aws.eks.Cluster", "aws.eks.NodeGroup", "k8s.Provider",
				"k8s.apps.v1.Deployment", "k8s.core.v1.Service"},
			[]string{"aws.ecs.TaskDefinition", "aws.ecs.Service"}},
	} {
		t.Run(tc.platform, func(t *testing.T) {
			templates := resolveContainerUnit(t, tc.platform)
			for _, want := range tc.wants {
				if !templates[want] {
					t.Errorf("%s produced no %s; got %v", tc.platform, want, sortedSet(templates))
				}
			}
			for _, absent := range tc.absent {
				if templates[absent] {
					t.Errorf("%s produced %s, which belongs to the other platform", tc.platform, absent)
				}
			}
		})
	}
}

// The same portable memory reaches both platforms, in each one's units.
//
// Fargate writes plain numbers and Kubernetes writes a suffix and millicores,
// so this is a translation rather than a second setting -- and getting it wrong
// would silently give a pod a fraction of what its Fargate twin has.
func TestMemoryReachesBothPlatformsInTheRightUnits(t *testing.T) {
	cpu, memory, _, err := TaskDefinitionArgs("reporter", container(2048, nil), 256, 512)
	if err != nil {
		t.Fatal(err)
	}
	if cpu != "256" || memory != "2048" {
		t.Fatalf("fargate: cpu %q memory %q", cpu, memory)
	}
	// 1024 Fargate CPU units is one vCPU is 1000 millicores, so 256 is 250m.
	if got := millicores(cpu); got != "250m" {
		t.Errorf("millicores(%q) = %q, want 250m", cpu, got)
	}
	if got := millicores("1024"); got != "1000m" {
		t.Errorf("millicores(1024) = %q, want 1000m -- one vCPU", got)
	}
	// A value that is not a number must not silently become zero CPU.
	if got := millicores("many"); got != "250m" {
		t.Errorf("millicores of nonsense = %q, want the floor", got)
	}
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// resolveContainerUnit expands one container unit and returns the set of
// resource templates it produced.
func resolveContainerUnit(t *testing.T, platform string) map[string]bool {
	t.Helper()

	program := ir.NewProgram()
	unit := &ir.ExecUnit{Entrypoints: []string{"reporter.py"}}
	unit.ID = "reporter"
	if err := unit.Configure(config.ResourceConfig{
		Type:     config.TypeContainer,
		Platform: platform,
		Memory:   2048,
	}); err != nil {
		t.Fatal(err)
	}
	program.AddIntent(unit)

	r := &Resolver{App: "shop", Program: program, Config: config.New()}
	if err := r.Resolve(); err != nil {
		t.Fatalf("resolving a %s container unit: %v", platform, err)
	}

	out := map[string]bool{}
	for _, res := range program.Resources() {
		out[res.Template()] = true
	}
	return out
}
