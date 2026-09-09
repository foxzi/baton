package main

import (
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/foxzi/baton/internal/exitcode"
	"github.com/foxzi/baton/internal/scenario"
)

// initTemplates embeds every file `baton init` can write. Each template
// directory is self-contained: a scenario plus whatever prompt, schema and
// config files it needs to run outside this repository.
//
//go:embed templates/hello templates/summarize
var initTemplates embed.FS

const initUsage = `Usage: baton init <directory> [options]

Writes a self-contained scenario into <directory>: the scenario file plus
every prompt, schema and config file it needs, ready to run outside this
repository. Existing files are never overwritten — the command checks every
target path first and refuses, before writing anything, if one is taken.

Also writes scenario.schema.json, this build's own scenario JSON Schema (the
same document "baton schema" prints), and points the scenario file at it with
a "# $schema: ./scenario.schema.json" comment at the top of the file, so
editors validate and autocomplete it without any network access: VS Code
(with the YAML extension), Neovim and other yaml-language-server clients
read that comment directly, and IntelliJ-based IDEs (IntelliJ IDEA, GoLand,
PyCharm, ...) read the same syntax as their own "$schema" convention. See
docs/en/editor-setup.md for details.

Options:
  --template NAME    hello (default) or summarize
  --provider NAME     llm provider for the summarize template: anthropic,
                      openai or openrouter (default: openrouter)
  --model NAME        llm model string for the summarize template
                      (default: the chosen provider's own default model)

Templates:
  hello       One "run" step that echoes a greeting and checks its exit
              code. No API key, no network, no global config.
  summarize   An "llm" step that summarizes text against a JSON schema,
              then a "file" step that writes the result to disk. The text
              to summarize and the output path are scenario inputs
              (-i text=..., -i out=...), overridable on any run. Needs a
              provider API key in the environment; the generated
              baton.yaml and the command's own output say which one.
`

// initProviderDefault is the model and the environment variable `baton
// init --template summarize` wires up for one provider.
type initProviderDefault struct {
	model  string
	envKey string
}

// initProviderDefaults lists the providers the summarize template supports.
// The provider name doubles as the config's provider kind (spec section
// 8.3), so init only needs to support the three built-in kinds that need no
// base_url of their own.
var initProviderDefaults = map[string]initProviderDefault{
	"anthropic":  {model: "anthropic/claude-haiku-4-5", envKey: "ANTHROPIC_API_KEY"},
	"openai":     {model: "openai/gpt-4.1-nano", envKey: "OPENAI_API_KEY"},
	"openrouter": {model: "openrouter/openai/gpt-4.1-nano", envKey: "OPENROUTER_API_KEY"},
}

// initTemplateNames lists the templates init supports, for suggesting a
// close match on an unknown one.
var initTemplateNames = []string{"hello", "summarize"}

// initSchemaFileName is the JSON Schema file `baton init` writes next to
// every scenario it generates, and the name the "$schema" comment atop the
// scenario file (see the hello and summarize templates) refers to by
// relative path.
const initSchemaFileName = "scenario.schema.json"

// initCmd implements `baton init`.
func initCmd(args []string) int {
	var (
		templateName string
		provider     string
		model        string
	)
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() { fmt.Fprint(os.Stderr, initUsage) }
	flags.StringVar(&templateName, "template", "hello", "template name")
	flags.StringVar(&provider, "provider", "openrouter", "llm provider (summarize template only)")
	flags.StringVar(&model, "model", "", "llm model string (summarize template only)")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return flagsExitCode(err)
	}
	if len(positional) != 1 {
		fmt.Fprint(os.Stderr, initUsage)
		return exitcode.Config
	}
	dir := positional[0]

	root := "templates/" + templateName
	if templateName != "hello" && templateName != "summarize" {
		fmt.Fprintf(os.Stderr, "%s, want hello or summarize\n", unknownCommandError("template", templateName, initTemplateNames))
		return exitcode.Config
	}

	// --provider/--model only mean anything for summarize; passing either
	// one with hello would otherwise be silently ignored, which looks like
	// a bug more than a no-op.
	var explicitProvider, explicitModel bool
	flags.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "provider":
			explicitProvider = true
		case "model":
			explicitModel = true
		}
	})
	if templateName == "hello" && (explicitProvider || explicitModel) {
		fmt.Fprint(os.Stderr, "baton: --provider/--model apply only to --template summarize, not hello\n")
		return exitcode.Config
	}

	replacer, envKey, err := initReplacer(templateName, provider, model)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}

	files, err := initTemplateFiles(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}

	conflicts, err := initConflicts(dir, root, files)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}
	// scenario.schema.json is not one of the embedded template files (its
	// content is generated, not copied), but it lands in the same
	// directory, so it must clear the same "nothing is overwritten"
	// check before anything is written.
	schemaTarget := filepath.Join(dir, initSchemaFileName)
	if _, err := os.Lstat(schemaTarget); err == nil {
		conflicts = append(conflicts, schemaTarget)
		sort.Strings(conflicts)
	} else if !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}
	if len(conflicts) > 0 {
		fmt.Fprintf(os.Stderr, "baton: refusing to overwrite %s:\n", plural(len(conflicts), "existing file"))
		for _, c := range conflicts {
			fmt.Fprintf(os.Stderr, "  %s\n", c)
		}
		return exitcode.Config
	}

	if err := initWriteFiles(dir, root, files, replacer); err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}
	if err := initWriteSchemaFile(schemaTarget); err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}

	scenarioPath := filepath.Join(dir, templateName+".yaml")
	fmt.Printf("wrote %s template to %s\n", templateName, dir)
	if templateName == "hello" {
		fmt.Printf("next: baton run %s\n", shellQuote(scenarioPath))
	} else {
		// baton.yaml is only auto-loaded from the current directory (spec
		// section 12), never resolved relative to the scenario file. Running
		// `baton run <dir>/summarize.yaml` from outside dir would silently
		// miss the provider config just written there, so the suggested
		// command cds into dir first.
		fmt.Printf("next: export %s=... then cd %s && baton run %s.yaml\n", envKey, shellQuote(dir), templateName)
	}
	return exitcode.OK
}

// initReplacer builds the token substitution for the summarize template's
// provider, model and API key environment variable. hello needs none of
// this, so it gets a no-op replacer.
func initReplacer(templateName, provider, model string) (*strings.Replacer, string, error) {
	if templateName != "summarize" {
		return strings.NewReplacer(), "", nil
	}
	def, ok := initProviderDefaults[provider]
	if !ok {
		names := make([]string, 0, len(initProviderDefaults))
		for name := range initProviderDefaults {
			names = append(names, name)
		}
		sort.Strings(names)
		return nil, "", fmt.Errorf("unknown provider %q, want one of %s", provider, strings.Join(names, ", "))
	}
	if model == "" {
		model = def.model
	}
	// model ends up as an unquoted YAML plain scalar in summarize.yaml
	// (`model: __BATON_MODEL__`). A user-supplied --model can contain
	// anything - a colon, a leading "#", quotes, a newline - any of which
	// would either break the YAML or be silently reinterpreted. A JSON
	// string is a valid YAML double-quoted scalar, so marshal it instead of
	// substituting the raw value; json.Marshal of a string never fails.
	modelJSON, err := json.Marshal(model)
	if err != nil {
		return nil, "", fmt.Errorf("encode model %q: %w", model, err)
	}
	replacer := strings.NewReplacer(
		"__BATON_PROVIDER__", provider,
		"__BATON_MODEL__", string(modelJSON),
		"__BATON_ENV_KEY__", def.envKey,
	)
	return replacer, def.envKey, nil
}

// initTemplateFiles lists the files under an embedded template root, in a
// stable order.
func initTemplateFiles(root string) ([]string, error) {
	var files []string
	err := fs.WalkDir(initTemplates, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

// initConflicts reports every target path that already exists, so initCmd
// can refuse before writing anything. It checks each path with Lstat, not
// Stat: a dangling symlink at a target has no Stat-able target of its own
// (Stat would report os.ErrNotExist and let initWriteFiles create a file
// through it), but it is still something already there that must not be
// touched. It also walks every ancestor directory a target would be created
// under - a plain file sitting where a template expects a directory (e.g. a
// pre-existing "schemas" file blocking "schemas/summarize.json") would
// otherwise only surface as a MkdirAll error midway through initWriteFiles,
// after other files of the same template were already written. Any ancestor
// that is itself a symlink is also refused, so init never follows a symlink
// directory to write outside dir.
func initConflicts(dir, root string, files []string) ([]string, error) {
	var conflicts []string
	seen := map[string]bool{}
	add := func(path string) {
		if !seen[path] {
			seen[path] = true
			conflicts = append(conflicts, path)
		}
	}

	for _, f := range files {
		rel := strings.TrimPrefix(f, root+"/")
		parts := strings.Split(rel, "/")

		blocked := false
		cur := dir
		for _, part := range parts[:len(parts)-1] {
			cur = filepath.Join(cur, part)
			info, err := os.Lstat(cur)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					break // nothing further down this path exists either
				}
				return nil, err
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				add(cur)
				blocked = true
				break
			}
		}
		if blocked {
			continue
		}

		target := filepath.Join(dir, rel)
		if _, err := os.Lstat(target); err == nil {
			add(target)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	sort.Strings(conflicts)
	return conflicts, nil
}

// initMkdirParents creates the directories a target file needs, one
// component at a time, refusing to descend through a symlink or a
// non-directory. initConflicts already checked this ahead of time, but a
// concurrent change between the check and the write (or plain caller misuse
// of these two functions) must not make init follow a symlink out of dir.
func initMkdirParents(dir string, parts []string) error {
	cur := dir
	for _, part := range parts {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				if mkErr := os.Mkdir(cur, 0o755); mkErr != nil {
					return mkErr
				}
				continue
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s: refusing to follow symlink", cur)
		}
		if !info.IsDir() {
			return fmt.Errorf("%s: not a directory", cur)
		}
	}
	return nil
}

// initWriteFiles writes every template file under dir, substituting tokens
// through replacer. Called only after initConflicts found nothing in the
// way. plural (used above for the error message) is defined in validate.go.
//
// Each file is opened with O_EXCL: initConflicts runs strictly before any
// write, so between that check and this call something else could have
// created the same path (another process, a concurrent `baton init`).
// O_CREATE|O_EXCL turns that race into a clean error instead of silently
// overwriting whatever showed up in the meantime.
func initWriteFiles(dir, root string, files []string, replacer *strings.Replacer) error {
	// dir itself is the path the caller asked for; creating it (and its own
	// ancestors) follows the ordinary mkdir -p rules. Only directories
	// created inside it - "schemas", "prompts" - go through
	// initMkdirParents, which refuses to follow a symlink.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	for _, f := range files {
		rel := strings.TrimPrefix(f, root+"/")
		parts := strings.Split(rel, "/")
		target := filepath.Join(dir, rel)

		if err := initMkdirParents(dir, parts[:len(parts)-1]); err != nil {
			return err
		}
		raw, err := initTemplates.ReadFile(f)
		if err != nil {
			return err
		}
		content := replacer.Replace(string(raw))

		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		_, writeErr := out.Write([]byte(content))
		closeErr := out.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

// initWriteSchemaFile writes this build's own scenario JSON Schema (the
// same document `baton schema` prints) to target, so the "$schema" comment
// atop the generated scenario resolves to a local file instead of a URL
// that may not stay published, reachable or in step with this binary's
// version of the format. Like initWriteFiles it refuses to replace an
// existing file: initCmd already checked target's absence as part of the
// same up-front conflict check, but O_EXCL keeps the guarantee even if the
// file appeared in the small window since.
func initWriteSchemaFile(target string) error {
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, writeErr := out.Write(scenario.Schema())
	closeErr := out.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}
