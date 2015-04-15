package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
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

func (e *eachFileLister) filteredFilename(filename string) bool {
	if filename == "" {
		return true
	}
	for filename != "" && filename != "." {
		var subdir string
		filename, subdir = path.Split(filename)
		filename = path.Clean(filename)
		for _, d := range e.IgnoreDir {
			if d == subdir {
				return true
			}
		}
	}
	return false
}

type checkCmd struct {
	Cmd  string   `json:"cmd"`
	Args []string `json:"args"`
}

type check struct {
	Name string `json:"name"`
	Cmd  string `json:"cmd"`

	Fix     *checkCmd `json:"fix"`
	Check   *checkCmd `json:"check"`
	Install *checkCmd `json:"install"`
	Gotool  string    `json:"gotool"`

	Each    *eachFileLister `json:"each"`
	Options json.RawMessage

	IgnoreMsg []string `json:"ignoreMsg"`

	Validator       json.RawMessage `json:"validate"`
	validateDecoded cmdValidator
}

type validator struct {
	Type string `json:"type"`
}

type config struct {
	Checks           []check  `json:"checks"`
	IgnoreDir        []string `json:"ignoreDir"`
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

func (p *goverify) main() error {
	if p.verbose {
		p.logger = log.New(os.Stderr, "", log.LstdFlags)
	} else {
		p.logger = log.New(ioutil.Discard, "", log.LstdFlags)
	}
	var conf config
	fileContent, err := ioutil.ReadFile(p.configFile)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(fileContent, &conf); err != nil {
		return err
	}
	if conf.SimultaneousRuns == 0 {
		conf.SimultaneousRuns = runtime.NumCPU()*2 + 1
	}
	fp, err := filepath.Abs(p.configFile)
	if err != nil {
		return err
	}
	conf.rootPath = filepath.Dir(fp)
	for _, c := range conf.Checks {
		if err = p.checkStream(conf, c); err != nil {
			return err
		}
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
	c.validateDecoded, err = p.getValidator(c)
	if err != nil {
		return err
	}
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
			IgnoreMsg: c.IgnoreMsg,
		}, nil
	}
	var dest cmdValidator
	var v validator
	if err := json.Unmarshal(c.Validator, &v); err != nil {
		return nil, err
	}
	if v.Type == "cover" {
		dest = &coverageValidator{}
	} else {
		dest = &emptyValidator{
			IgnoreMsg: c.IgnoreMsg,
		}
	}
	if err := json.Unmarshal(c.Validator, dest); err != nil {
		return nil, err
	}
	return dest, nil
}

type cmdValidator interface {
	Check(stdout *bytes.Buffer, stderr *bytes.Buffer) error
}

type emptyValidator struct {
	IgnoreMsg []string
}

func (c *emptyValidator) Check(stdout *bytes.Buffer, stderr *bytes.Buffer) error {
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
	RequiredCoverage float64 `json:"coverage"`
}

type coverageError struct {
	seen     float64
	required float64
}

func (c *coverageError) Error() string {
	return fmt.Sprintf("Coverage %f less than required %f", c.seen, c.required)
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
	p.logger.Printf("Running check %s\n", c.Name)
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
	p.logger.Printf("Running command %s %s\n", c.Cmd, args)
	cmd := exec.Command(c.Cmd, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
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
