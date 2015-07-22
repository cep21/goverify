package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

type checkResult struct {
	checkName   string
	output      string
	originalErr error
}

func (c *checkResult) Error() string {
	return fmt.Sprintf("%s\n%s\n%s", c.checkName, c.originalErr, c.output)
}

type eachFileLister struct {
	Cmd       string   `json:"cmd"`
	Args      []string `json:"args"`
	IgnoreDir []string `json:"ignoreDir"`
}

func (e *eachFileLister) String() string {
	return fmt.Sprintf("Cmd: %s | Args: %s | IgnoreDir: %s", e.Cmd, e.Args, e.IgnoreDir)
}

func mergeEachFileLister(e1, e2 *eachFileLister) *eachFileLister {
	if e1 == nil {
		return e2
	}
	if e2 == nil {
		return e1
	}
	return &eachFileLister{
		Cmd:       nonEmptyStr(e1.Cmd, e2.Cmd),
		Args:      nonEmptyStrArr(e1.Args, e2.Args),
		IgnoreDir: nonEmptyStrArr(e1.IgnoreDir, e2.IgnoreDir),
	}
}

func containsName(filename string, searchIn []string) bool {
	for filename != "" && filename != "." {
		var subdir string
		filename, subdir = path.Split(filename)
		filename = path.Clean(filename)
		for _, d := range searchIn {
			if d == subdir {
				return true
			}
		}
	}
	return false
}

func (e *eachFileLister) filteredFilename(filename string) bool {
	if filename == "" {
		return true
	}
	return containsName(filename, e.IgnoreDir)
}

type checkCmd struct {
	Cmd  string   `json:"cmd"`
	Args []string `json:"args"`
}

func (c *checkCmd) String() string {
	return fmt.Sprintf("Cmd: %s | Args: %s", c.Cmd, c.Args)
}

func mergeCheckCmd(c1, c2 *checkCmd) *checkCmd {
	if c1 == nil {
		return c2
	}
	if c2 == nil {
		return c1
	}
	return &checkCmd{
		Cmd:  nonEmptyStr(c1.Cmd, c2.Cmd),
		Args: nonEmptyStrArr(c1.Args, c2.Args),
	}
}

type check struct {
	Name string `json:"name"`
	Cmd  string `json:"cmd"`

	Fix     *checkCmd `json:"fix"`
	Check   *checkCmd `json:"check"`
	Install *checkCmd `json:"install"`

	Gotool string `json:"gotool"`
	Godep  *bool  `json:"godep"`
	Macro  string `json:"macro"`

	Each *eachFileLister `json:"each"`

	Validator       json.RawMessage `json:"validate"`
	validateDecoded cmdValidator
}

func (c *check) String() string {
	return fmt.Sprintf("Name: %s | Cmd: %s | Fix: %s | Check: %s | Install: %s | Gotool: %s | Macro: %s | Each: %s | Validator: %s", c.Name, c.Cmd, c.Fix, c.Check, c.Install, c.Gotool, c.Macro, c.Each, c.Validator)
}

func (c *check) mergePropertiesFrom(macroDef check) {
	c.Name = nonEmptyStr(c.Name, macroDef.Name)
	c.Cmd = nonEmptyStr(c.Cmd, macroDef.Cmd)

	c.Fix = mergeCheckCmd(c.Fix, macroDef.Fix)
	c.Check = mergeCheckCmd(c.Check, macroDef.Check)
	c.Install = mergeCheckCmd(c.Install, macroDef.Install)

	c.Gotool = nonEmptyStr(c.Gotool, macroDef.Gotool)
	if c.Godep == nil {
		c.Godep = macroDef.Godep
	}

	c.Each = mergeEachFileLister(c.Each, macroDef.Each)

	_, unsetValidator := c.validateDecoded.(*emptyValidator)
	if unsetValidator {
		c.validateDecoded = nil
	}
}

func nonEmptyStr(s1, s2 string) string {
	if s1 == "" {
		return s2
	}
	return s1
}

func nonEmptyStrArr(s1, s2 []string) []string {
	if len(s1) == 0 {
		return s2
	}
	return s1
}

type validator struct {
	Type string `json:"type"`
}

type config struct {
	Checks           []check          `json:"checks"`
	Macros           map[string]check `json:"macros"`
	IgnoreDir        []string         `json:"ignoreDir"`
	rootPath         string
	SimultaneousRuns int `json:"simultaneousRuns"`
	GlobalIgnore     []string
}

type goverify struct {
	configFile string
	fix        bool
	rootDir    string
	verbose    bool
	logger     *log.Logger

	cmdStdout io.Writer
	cmdStderr io.Writer

	run runCommand
}

var primaryMain = goverify{
	run: run,
}

func init() {
	flag.StringVar(&primaryMain.configFile, "config", "goverify.json", "config file for building")
	flag.BoolVar(&primaryMain.fix, "fix", false, "If true, also fix the code if it can")
	flag.BoolVar(&primaryMain.verbose, "v", false, "If true, verbose output")
	flag.Parse()
}

func main() {
	if err := primaryMain.main(); err != nil {
		fmt.Printf("%s\n", err)
		os.Exit(1)
	}
}

func (p *goverify) loadMacros(conf *config) error {
	var err error
	var macro config
	if err = json.Unmarshal([]byte(macros), &macro); err != nil {
		return err
	}
	if conf.Macros == nil {
		conf.Macros = make(map[string]check)
	}
	conf.Checks = append(macro.Checks, conf.Checks...)
	for k, v := range macro.Macros {
		v.validateDecoded, err = p.getValidator(v)
		if err != nil {
			return err
		}
		conf.Macros[k] = v
	}
	return nil
}

func (p *goverify) loadConfig() (*config, error) {
	var conf config
	fileContent, err := ioutil.ReadFile(p.configFile)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(fileContent, &conf); err != nil {
		return nil, err
	}
	if err = p.loadMacros(&conf); err != nil {
		return nil, err
	}
	if conf.SimultaneousRuns == 0 {
		conf.SimultaneousRuns = runtime.NumCPU()*2 + 1
	}
	fp, err := filepath.Abs(p.configFile)
	if err != nil {
		return nil, err
	}
	conf.rootPath = filepath.Dir(fp)
	return &conf, nil
}

func (p *goverify) main() error {
	if p.verbose {
		p.logger = log.New(os.Stderr, "", log.LstdFlags)
		p.cmdStdout = os.Stdout
		p.cmdStderr = os.Stderr
	} else {
		p.logger = log.New(ioutil.Discard, "", log.LstdFlags)
		p.cmdStdout = ioutil.Discard
		p.cmdStderr = ioutil.Discard
	}
	conf, err := p.loadConfig()
	if err != nil {
		return err
	}
	for _, c := range conf.Checks {
		c.validateDecoded, err = p.getValidator(c)
		if err != nil {
			return err
		}
		if c.Macro != "" {
			if err = p.copyFromMacro(conf, &c); err != nil {
				return err
			}
		}
		if cover, ok := c.validateDecoded.(*coverageValidator); ok {
			cover.IgnoreDir = conf.IgnoreDir
		}
		if err = p.checkStream(*conf, c); err != nil {
			return err
		}
	}
	return nil
}

func (p *goverify) copyFromMacro(conf *config, c *check) error {
	existingMacro, exists := conf.Macros[c.Macro]
	if !exists {
		return fmt.Errorf("unable to find macro %s", c.Macro)
	}
	p.logger.Printf("Loading properties for macro %s", c.Macro)
	c.mergePropertiesFrom(existingMacro)
	if c.validateDecoded == nil {
		c.validateDecoded, _ = p.getValidator(existingMacro)
		c.validateDecoded.MergePropertiesFrom(c.Validator)
	}
	if c.Macro == "go-cover" {
		_ = c.validateDecoded.(*coverageValidator)
	}
	return nil
}

func (p *goverify) installToolIfNeeded(conf config, c check) error {
	toolFound := true
	if c.Gotool != "" {
		toolBytes, err := exec.Command("go", "tool").CombinedOutput()
		if err != nil {
			return err
		}
		toolFound = func() bool {
			for _, tool := range strings.Split(string(toolBytes), "\n") {
				if tool == c.Gotool {
					return true
				}
			}
			return false
		}()
	}
	_, err := exec.LookPath(c.Cmd)
	if c.Install != nil && (err != nil || !toolFound) {
		p.logger.Printf("Installing %s %s", c.Install.Cmd, c.Install.Args)
		// Try to install
		if err = exec.Command(c.Install.Cmd, c.Install.Args...).Run(); err != nil {
			return err
		}
	}
	return nil
}

func (p *goverify) checkStream(conf config, c check) error {
	var err error
	if c.Each != nil {
		c.Each.IgnoreDir = append(c.Each.IgnoreDir, conf.IgnoreDir...)
	}
	if err = p.installToolIfNeeded(conf, c); err != nil {
		return err
	}
	checkOutput := p.runCheck(conf, c)
	var lastError error
	for checkRes := range checkOutput {
		if checkRes.originalErr != nil {
			lastError = checkRes.originalErr
			fmt.Printf("%s\n", strings.TrimSpace(checkRes.output))
		}
	}
	if lastError != nil {
		return lastError
	}
	return nil
}

func (p *goverify) getValidator(c check) (cmdValidator, error) {
	if c.Validator == nil {
		return &emptyValidator{
			IgnoreMsg: []string{},
		}, nil
	}
	var dest cmdValidator
	var v validator
	if err := json.Unmarshal(c.Validator, &v); err != nil {
		return nil, err
	}
	if v.Type == "cover" {
		dest = &coverageValidator{}
	} else if v.Type == "returncode" {
		dest = &emptyValidator{
			IgnoreMsg:       []string{},
			IgnoreAllOutput: true,
		}
	} else {
		dest = &emptyValidator{
			IgnoreMsg: []string{},
		}
	}
	if err := json.Unmarshal(c.Validator, dest); err != nil {
		return nil, err
	}
	return dest, nil
}

type cmdValidator interface {
	Check(stdout *bytes.Buffer, stderr *bytes.Buffer) error
	MergePropertiesFrom(val json.RawMessage)
}

type emptyValidator struct {
	validator
	IgnoreMsg       []string `json:"ignoreMsg"`
	IgnoreAllOutput bool     `json:"ignoreOutput"`
}

func (c *emptyValidator) MergePropertiesFrom(val json.RawMessage) {
	if val == nil {
		return
	}
	var other emptyValidator
	if err := json.Unmarshal(val, &other); err != nil {
		return
	}
	c.IgnoreMsg = nonEmptyStrArr(other.IgnoreMsg, c.IgnoreMsg)
}

func (c *emptyValidator) Check(stdout *bytes.Buffer, stderr *bytes.Buffer) error {
	if c.IgnoreAllOutput {
		return nil
	}
	if stderr.Len() > 0 {
		return errors.New("non empty stderr")
	}
	for _, line := range strings.Split(stdout.String(), "\n") {
		errOutput := func() bool {
			if line == "" {
				return false
			}
			for _, ig := range c.IgnoreMsg {
				if strings.Contains(line, ig) {
					return false
				}
			}
			return true
		}()
		if errOutput {
			return errors.New("unexpected output")
		}
	}
	return nil
}

type coverageValidator struct {
	validator
	RequiredCoverage float64  `json:"coverage"`
	IgnoreDir        []string `json:"ignoreDir"`
}

type coverageError struct {
	seen     float64
	required float64
}

func (c *coverageError) Error() string {
	return fmt.Sprintf("Coverage %f less than required %f", c.seen, c.required)
}

func (c *coverageValidator) MergePropertiesFrom(val json.RawMessage) {
	if val == nil {
		return
	}
	var other coverageValidator
	if err := json.Unmarshal(val, &other); err != nil {
		return
	}
	if other.RequiredCoverage != 0 {
		c.RequiredCoverage = other.RequiredCoverage
	}
	c.IgnoreDir = nonEmptyStrArr(other.IgnoreDir, c.IgnoreDir)
}

func (c *coverageValidator) Check(stdout *bytes.Buffer, stderr *bytes.Buffer) error {
	pattern := regexp.MustCompile(`coverage: ([0-9\.]+)% of statements`)
	for _, coverout := range strings.Split(stdout.String(), "\n") {
		if coverout == "" {
			continue
		}
		matchPercent, err := func() (float64, error) {
			if strings.Contains(coverout, "[no test files]") {
				return 0.0, nil
			}
			matches := pattern.FindStringSubmatch(coverout)
			if matches == nil {
				return 0.0, fmt.Errorf("unable to find match in string: %s", coverout)
			}
			return strconv.ParseFloat(matches[1], 64)
		}()
		if err != nil {
			return err
		}
		parts := strings.Split(coverout, "\t")
		if len(parts) > 1 {
			testPath := parts[1]
			if containsName(testPath, c.IgnoreDir) {
				continue
			}
		}
		if matchPercent+.009 <= c.RequiredCoverage {
			return &coverageError{
				seen:     matchPercent,
				required: c.RequiredCoverage,
			}
		}
	}
	return nil
}

func (p *goverify) runCheck(conf config, c check) chan checkResult {
	p.logger.Printf("Running check `%s`", c.String())
	var params []string
	var err error
	checkOutput := make(chan checkResult)
	if c.Each != nil {
		params, err = p.getParams(conf, c)
		if err != nil {
			go func() {
				checkOutput <- checkResult{
					originalErr: err,
				}
				close(checkOutput)
			}()
			return checkOutput
		}
	} else {
		params = []string{"."}
	}
	paramOptions := make(chan string)

	go func() {
		for _, param := range params {
			paramOptions <- param
		}
		close(paramOptions)
	}()
	var wg sync.WaitGroup
	for i := 0; i < conf.SimultaneousRuns; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for param := range paramOptions {
				checkRes := p.innerCheckIteration(conf, c, param)
				if p.fix && c.Fix != nil && checkRes.originalErr != nil {
					//  Try to fix it again
					checkRes = p.innerCheckIteration(conf, c, param)
				}
				checkOutput <- checkRes
			}
		}()
	}
	go func() {
		wg.Wait()
		close(checkOutput)
	}()
	return checkOutput
}

type runCommand func(*exec.Cmd) error

func run(cmd *exec.Cmd) error {
	return cmd.Run()
}

func hasGodepDirectory() bool {
	_, err := os.Stat("Godeps")
	if err == nil {
		return true
	}
	if os.IsNotExist(err) {
		return false
	}
	return false
}

func (p *goverify) innerCheckIteration(conf config, c check, param string) checkResult {
	args := func() []string {
		if p.fix && c.Fix != nil {
			return append(make([]string, 0, len(c.Fix.Args)), c.Fix.Args...)
		}
		return append(make([]string, 0, len(c.Check.Args)), c.Check.Args...)
	}()
	for i := range args {
		if args[i] == "$1" {
			args[i] = param
		}
	}
	var cmdToRun string
	if c.Godep != nil && *c.Godep && hasGodepDirectory() {
		cmdToRun = "godep"
		args = append([]string{"go"}, args...)
	} else {
		cmdToRun = c.Cmd
	}
	p.logger.Printf("Running command %s %s %v\n", cmdToRun, args, &c)
	cmd := exec.Command(cmdToRun, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = io.MultiWriter(&stdout, p.cmdStdout)
	cmd.Stderr = io.MultiWriter(&stderr, p.cmdStderr)
	err := p.run(cmd)
	output := stdout.String() + stderr.String()
	if err != nil {
		return checkResult{
			originalErr: err,
			output:      output,
		}
	}
	if err = c.validateDecoded.Check(&stdout, &stderr); err != nil {
		return checkResult{
			originalErr: err,
			output:      output,
		}
	}
	return checkResult{
		output: output,
	}
}

func (p *goverify) getParams(conf config, c check) ([]string, error) {
	cmd := exec.Command(c.Each.Cmd, c.Each.Args...)
	cmd.Dir = p.rootDir
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := p.run(cmd); err != nil {
		return nil, &checkResult{
			output:      stdout.String() + stderr.String(),
			originalErr: err,
		}
	}
	files := []string{}
	for _, file := range strings.Split(stdout.String(), "\n") {
		if !c.Each.filteredFilename(file) {
			files = append(files, file)
		}
	}
	return files, nil
}
