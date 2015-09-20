package main

import (
	"bytes"
	"io/ioutil"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func noError(t *testing.T, err error) {
	if err != nil {
		t.Errorf("Error: %s\n", err)
		panic("")
	}
}

func errorSeen(t *testing.T, err error) {
	if err == nil {
		t.Errorf("Expect error, but got none!")
		panic("")
	}
}

func TestCoverageValidator(t *testing.T) {
	c := coverageValidator{
		RequiredCoverage: 10.0,
	}
	stderr := new(bytes.Buffer)
	stdout := bytes.NewBufferString("ok  	github.com/signalfx/metricproxy	0.052s	coverage: 100.0% of statements\n")
	noError(t, c.Check(stdout, stderr))

	stdout = bytes.NewBufferString("ok  	github.com/signalfx/metricproxy	0.052s	coverage: 10.1% of statements\n")
	noError(t, c.Check(stdout, stderr))

	stdout = bytes.NewBufferString("ok  	github.com/signalfx/metricproxy	0.052s	coverage: 9.0% of statements\n")
	errorSeen(t, c.Check(stdout, stderr))
}

var t1 = `{
  "checks": [
    {
      "name": "import fix",
      "cmd": "goimports",
      "fix": {
        "args": ["-w", "-l", "$1"]
      },
      "check": {
        "args": ["-l", "$1"]
      },
      "each": {
        "cmd": "git",
        "args": ["ls-files", "--", "*.go"]
      }
    }
  ]
}`

func panicIfNotNil(i interface{}) {
	if i != nil {
		panic(i)
	}
}

func panicIfNotNil2(_ interface{}, i interface{}) {
	if i != nil {
		panic(i)
	}
}

func TestSimpleCover(t *testing.T) {
	fout, err := ioutil.TempFile("", "TestSimpleCover")
	noError(t, err)
	filename := fout.Name()
	defer func() { panicIfNotNil(os.Remove(filename)) }()
	panicIfNotNil(fout.Close())
	noError(t, ioutil.WriteFile(filename, []byte(t1), os.FileMode(0600)))
	m := &goverify{
		run: func(cmd *exec.Cmd) error {
			if strings.HasSuffix(cmd.Path, "git") {
				panicIfNotNil2(cmd.Stdout.Write([]byte("hello.go")))
			}
			return nil
		},
		configFile: filename,
		verbose:    true,
	}
	noError(t, m.main())
}

func TestEachFileLister(t *testing.T) {
	l := eachFileLister{
		IgnoreDir: []string{"abcd"},
	}
	if !l.filteredFilename("") {
		panic("Expect to filter empty")
	}
	if !l.filteredFilename("testing/abcd/test.go") {
		panic("Expect to filter abcd")
	}
	if l.filteredFilename("testing/abcde/test.go") {
		panic("Expect not to filter abcde")
	}
}
