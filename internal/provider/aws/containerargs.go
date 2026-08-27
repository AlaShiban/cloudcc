package aws

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/cloudcompiler/cloudcc/internal/config"
)

// The container half of the two layers, and the reason a portable setting still
// has to be checked against the host it lands on.
//
// `memory:` means the same thing on both compute types -- megabytes the
// application gets -- which is what makes it portable. What is not portable is
// which values are legal. Lambda takes anything from 128 to 10240 MB. Fargate
// takes a short ladder of values, and each rung is only available with certain
// amounts of CPU: 512 MB exists only at 0.25 vCPU, and 3072 MB does not exist
// at 0.25 vCPU at all.
//
// So the accepted set is the intersection of what the setting means and what
// the target can do. `memory: 1024` compiles on both; `memory: 128` compiles as
// a function and is an error as a container, naming the values Fargate has.
// Refusing it here is the whole point: AWS would otherwise reject the task
// definition at deploy time with `No Fargate configuration exists for given
// values`, which does not say which of the two numbers was wrong or what to put
// instead.

// TaskDefinitionResourceKey is the provider resource a container unit's sizing
// is written under.
const TaskDefinitionResourceKey = "aws.ecs.TaskDefinition"

// fargateSizes is Fargate's CPU-to-memory table, in the units the task
// definition uses: CPU in 1/1024ths of a vCPU, memory in MiB.
//
// A table rather than a formula because it is not one -- the step between
// legal memory values changes with CPU, and the ranges overlap. Sorted by CPU,
// which is what makes "the smallest CPU that can hold this memory" a scan.
var fargateSizes = []struct {
	cpu      int
	memories []int
}{
	{256, span(512, 1024, 2048)},
	{512, step(1024, 4096, 1024)},
	{1024, step(2048, 8192, 1024)},
	{2048, step(4096, 16384, 1024)},
	{4096, step(8192, 30720, 1024)},
	{8192, step(16384, 61440, 4096)},
	{16384, step(32768, 122880, 8192)},
}

func span(values ...int) []int { return values }

func step(low, high, by int) []int {
	var out []int
	for v := low; v <= high; v += by {
		out = append(out, v)
	}
	return out
}

// taskArgs is the AWS-specific surface for a container unit.
//
// `memory` is absent for the same reason it is absent from lambdaArgs: it is
// the portable setting, written on the unit. `cpu` is here rather than beside
// it because Lambda has no CPU dial at all -- it allocates CPU in proportion to
// memory -- so a portable `cpu:` would be a setting that cannot be honoured on
// one of the two compute types this compiler already supports.
var taskArgs = []lambdaArg{
	{
		Name: "cpu", Pulumi: "cpu", Kind: argInt,
		Why:   "CPU units, 1024 to the vCPU; Fargate takes 256, 512, 1024, 2048, 4096, 8192 or 16384",
		Check: validFargateCPU,
	},
	{
		Name: "ephemeral_storage", Pulumi: "ephemeralStorage", Kind: argBlock,
		Why: "how much scratch space the task has",
		Fields: []lambdaArg{{
			Name: "size_in_gib", Pulumi: "sizeInGib", Kind: argInt,
			Why:   "gibibytes of scratch space, 21 to 200",
			Check: intBetween(21, 200, "GiB"),
		}},
	},
}

func validFargateCPU(v any) string {
	n, ok := asInt(v)
	if !ok {
		return "must be a whole number"
	}
	var legal []string
	for _, size := range fargateSizes {
		if size.cpu == n {
			return ""
		}
		legal = append(legal, strconv.Itoa(size.cpu))
	}
	return fmt.Sprintf("must be one of %s; Fargate has no other sizes, and %d is not one of them",
		strings.Join(legal, ", "), n)
}

// TaskDefinitionArgs validates a container unit's configuration, both layers,
// and returns the CPU and memory the task definition should carry plus any
// remaining provider arguments.
//
// Fargate requires both numbers and will not infer either, so a unit that sets
// only `memory:` gets the smallest CPU that can hold it. That is a default
// rather than a guess: it is the cheapest legal answer, it is written into the
// generated project where it can be read, and saying so beats making every unit
// that wants more memory also name a CPU it has no opinion about.
func TaskDefinitionArgs(unitID string, cfg config.ResourceConfig, defaultCPU, defaultMemory int) (string, string, map[string]any, error) {
	if cfg.Timeout != 0 {
		return "", "", nil, fmt.Errorf("execution unit %q is type %s, and `timeout:` does not "+
			"apply to it. A timeout is how long one invocation may run before it is killed; "+
			"a container service is not invoked, it stays up. Whatever this was meant to "+
			"bound belongs in the application",
			unitID, config.TypeContainer)
	}

	for key := range cfg.ProviderArgs {
		if key != TaskDefinitionResourceKey {
			return "", "", nil, fmt.Errorf("execution unit %q: %q is not a resource this unit "+
				"becomes. A unit of type %s on AWS is sized by %s",
				unitID, key, config.TypeContainer, TaskDefinitionResourceKey)
		}
	}
	block := cfg.ProviderArgs[TaskDefinitionResourceKey]
	for name := range block {
		if name == "memory" {
			return "", "", nil, fmt.Errorf("execution unit %q: %s.memory is the portable "+
				"setting `memory:`, which belongs on the unit itself:\n"+
				"    %s:\n      type: %s\n      memory: ...\n"+
				"  It means the same thing on a function and on a container, which is why it "+
				"is written in one place -- what differs is which values the target accepts, "+
				"and that is checked either way",
				unitID, TaskDefinitionResourceKey, unitID, config.TypeContainer)
		}
	}

	extra, err := translate(unitID, "", block, taskArgs)
	if err != nil {
		return "", "", nil, err
	}

	memory := defaultMemory
	if cfg.Memory != 0 {
		memory = cfg.Memory
	}
	cpu := defaultCPU
	requested, explicit := extra["cpu"].(int)
	if explicit {
		cpu = requested
		delete(extra, "cpu")
	}

	// A memory the platform does not have at any CPU is worth its own message:
	// telling someone their memory does not go with their CPU, when in fact it
	// goes with nothing, sends them adjusting the wrong number.
	if !memoryExistsAtSomeCPU(memory) {
		return "", "", nil, fmt.Errorf("execution unit %q: memory %d MB is not a size Fargate "+
			"has. It takes 512, then multiples of 1024 from 1024 to %d, then larger sizes at "+
			"higher CPU -- and %s at %d CPU units, which is the smallest task.\n"+
			"  A function's range is continuous from 128 MB, so this is a value that compiles "+
			"as `type: %s` and cannot as `type: %s`",
			unitID, memory, 30720, quotedInts(fargateSizes[0].memories), fargateSizes[0].cpu,
			config.TypeFunction, config.TypeContainer)
	}

	if !explicit {
		cpu = smallestCPUFor(memory)
	} else if !pairIsLegal(cpu, memory) {
		return "", "", nil, fmt.Errorf("execution unit %q: Fargate has no configuration with "+
			"%d CPU units and %d MB of memory. At %d CPU units it takes %s.\n"+
			"  Leaving `cpu` out picks the smallest that fits, which for %d MB is %d",
			unitID, cpu, memory, cpu, describeMemories(cpu), memory, smallestCPUFor(memory))
	}

	// Strings, because that is what the task definition takes -- a number here
	// is rejected by the API rather than coerced.
	return strconv.Itoa(cpu), strconv.Itoa(memory), extra, nil
}

func memoryExistsAtSomeCPU(memory int) bool {
	for _, size := range fargateSizes {
		for _, m := range size.memories {
			if m == memory {
				return true
			}
		}
	}
	return false
}

func smallestCPUFor(memory int) int {
	for _, size := range fargateSizes {
		for _, m := range size.memories {
			if m == memory {
				return size.cpu
			}
		}
	}
	return fargateSizes[0].cpu
}

func pairIsLegal(cpu, memory int) bool {
	for _, size := range fargateSizes {
		if size.cpu != cpu {
			continue
		}
		for _, m := range size.memories {
			if m == memory {
				return true
			}
		}
	}
	return false
}

// describeMemories renders what one CPU size accepts, collapsing a long
// arithmetic run rather than printing thirty numbers at a reader.
func describeMemories(cpu int) string {
	for _, size := range fargateSizes {
		if size.cpu != cpu {
			continue
		}
		if len(size.memories) <= 4 {
			return quotedInts(size.memories)
		}
		by := size.memories[1] - size.memories[0]
		return fmt.Sprintf("%d to %d in steps of %d",
			size.memories[0], size.memories[len(size.memories)-1], by)
	}
	return ""
}

func quotedInts(values []int) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = strconv.Itoa(v)
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " or " + parts[len(parts)-1]
}

// ConfigurableResources lists the provider resources a compute type exposes,
// for the diagnostic shown when a block names something else.
func ConfigurableResources(computeType string) []string {
	switch computeType {
	case config.TypeFunction:
		return []string{FunctionResourceKey}
	case config.TypeContainer:
		return []string{TaskDefinitionResourceKey}
	}
	return nil
}
