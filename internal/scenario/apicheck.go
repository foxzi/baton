package scenario

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/foxzi/baton/internal/packs"
)

// CheckPacks loads the packs a scenario binds and checks the scenario
// against them: checks 12 and 13 of spec section 4, plus the parts of check
// 14 that only the pack knows. It is kept apart from Validate because it
// reads the pack sources from disk and may fetch a git source, while
// Validate only looks at the scenario itself.
//
// The engine repeats these checks as it runs, since a pack named by a
// template is only known then.
func CheckPacks(scn *Scenario) Result {
	var res Result
	baseDir := filepath.Dir(scn.Path)
	if baseDir == "" {
		baseDir = "."
	}

	loaded := make(map[string]*packs.Pack, len(scn.APIs))
	for _, name := range sortedKeys(scn.APIs) {
		entry := scn.APIs[name]
		path := "apis." + name
		if strings.Contains(entry.Pack, "{{") {
			// The name is chosen at run time, so the candidates are not
			// known here; the engine checks the one it picks.
			res.warnf(path+".pack", entry.Line, "pack name is a template, so its operations are only checked at run time")
			continue
		}
		pack, err := packs.Load(packs.Source{
			From:    entry.From,
			Pack:    entry.Pack,
			SHA256:  entry.SHA256,
			BaseDir: baseDir,
		})
		if err != nil {
			res.errorf(path, entry.Line, "%v", err)
			continue
		}
		if entry.Interface != "" {
			if err := pack.CheckInterface(entry.Interface); err != nil {
				res.errorf(path+".interface", entry.Line, "%v", err)
				continue
			}
		}
		loaded[name] = pack
	}

	WalkSteps(scn, func(step *Step) {
		if step.HTTP != nil {
			checkHTTPOp(loaded, step, &res)
		}
		if step.Agent != nil && step.Agent.Tools != nil {
			checkToolOps(loaded, step, step.Agent.Tools.APIs, &res)
		}
	})
	return res
}

// checkHTTPOp checks the operation form of an http step against its pack
// (section 4, check 13).
func checkHTTPOp(loaded map[string]*packs.Pack, step *Step, res *Result) {
	apiName, opName := step.HTTP.APIName()
	if opName == "" {
		return
	}
	pack, ok := loaded[apiName]
	if !ok {
		// Either the api is undeclared, which Validate reports, or its pack
		// is a template, which CheckPacks already warned about.
		return
	}
	path := "steps." + step.ID + ".http"
	op, err := pack.Op(opName)
	if err != nil {
		res.errorf(path+".op", step.Line, "%v", err)
		return
	}

	for _, name := range sortedKeys(step.HTTP.Args) {
		if _, ok := op.Params[name]; !ok {
			res.errorf(fmt.Sprintf("%s.args.%s", path, name), step.Line, "operation %s has no such argument", opName)
		}
	}
	for _, name := range sortedKeys(op.Params) {
		if _, given := step.HTTP.Args[name]; !given && op.Params[name].IsRequired() {
			res.errorf(path+".args", step.Line, "operation %s requires the argument %s", opName, name)
		}
	}

	// Section 9.4, mirrored by check 14: an operation the pack does not
	// call readonly may change something, so repeating it needs a key.
	if !op.Readonly && step.DedupeKey == "" {
		res.warnf(path, step.Line, "operation %s is not readonly, so without dedupe_key it cannot be retried safely", opName)
	}
}

// checkToolOps checks the pack operations an agent step hands to the model.
// Only readonly operations may be given away (section 4, check 13), and an
// operation with no description leaves the model guessing (check 14).
func checkToolOps(loaded map[string]*packs.Pack, step *Step, refs []string, res *Result) {
	for i, ref := range refs {
		apiName, opName, found := strings.Cut(ref, ".")
		if !found {
			continue
		}
		pack, ok := loaded[apiName]
		if !ok {
			continue
		}
		path := fmt.Sprintf("steps.%s.agent.tools.apis[%d]", step.ID, i)
		op, err := pack.Op(opName)
		if err != nil {
			res.errorf(path, step.Line, "%v", err)
			continue
		}
		if !op.Readonly {
			res.errorf(path, step.Line, "operation %s is not readonly and cannot be given to a model", ref)
			continue
		}
		if strings.TrimSpace(op.Description) == "" {
			res.warnf(path, step.Line, "operation %s has no description, so the model has only its name to go by", ref)
		}
	}
}
