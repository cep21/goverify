package main

// List of predefined macros that users can use in their goverify.json file
var macros = `{
  "macros": {
    "goimport": {
      "name": "import fix",
      "cmd": "goimports",
      "fix": {
        "args": ["-w", "-l", "$1"]
      },
      "check": {
        "args": ["-l", "$1"]
      },
      "install": {
        "cmd": "go",
        "args": ["get", "golang.org/x/tools/cmd/goimports"]
      },
      "each": {
        "cmd": "git",
        "args": ["ls-files", "--", "*.go"]
      }
    },
    "gofmt": {
      "name": "fmt fix",
      "cmd": "gofmt",
      "fix": {
        "args": ["-s", "-w", "-l", "$1"]
      },
      "check": {
        "args": ["-s", "-l", "$1"]
      },
      "each": {
        "cmd": "git",
        "args": ["ls-files", "--", "*.go"]
      }
    },
    "vet": {
      "name": "vet",
      "cmd": "go",
      "check": {
        "args": ["tool", "vet", "$1"]
      },
      "gotool": "vet",
      "install": {
        "cmd": "go",
        "args": ["get", "golang.org/x/tools/cmd/vet"]
      },
      "each": {
        "cmd": "git",
        "args": ["ls-files", "--", "*.go"]
      }
    },
    "golint": {
      "name": "code lint",
      "cmd": "golint",
      "check": {
        "args": ["-min_confidence=.3", "$1"]
      },
      "install": {
        "cmd": "go",
        "args": ["get", "github.com/golang/lint/golint"]
      },
      "each": {
        "cmd": "git",
        "args": ["ls-files", "--", "*.go"]
      }
    },
    "gocyclo": {
      "name": "cyclomatic check",
      "cmd": "gocyclo",
      "check": {
        "args": ["-over", "10", "$1"]
      },
      "install": {
        "cmd": "go",
        "args": ["get", "github.com/fzipp/gocyclo"]
      },
      "each": {
        "cmd": "git",
        "args": ["ls-files", "--", "*.go"]
      }
    },
    "go-install": {
      "name": "Check that installs",
      "cmd": "go",
      "godep": true,
      "check": {
        "args": ["install", "."]
      }
    },
    "go-cover": {
      "name": "code coverage",
      "cmd": "go",
      "godep": true,
      "gotool": "cover",
      "install": {
        "cmd": "go",
        "args": ["get", "golang.org/x/tools/cmd/cover"]
      },
      "check": {
        "args": ["test", "-cover", "-covermode", "atomic", "-race", "-parallel=8", "-timeout", "3s", "-cpu", "4", "./..."]
      },
      "validate": {
        "type": "cover",
        "coverage": 100
      }
    },
    "gocoverdir": {
      "name": "code coverage with profile output",
      "cmd": "go",
      "install": {
        "cmd": "go",
        "args": ["get", "github.com/cep21/gocoverdir"]
      },
      "check": {
        "args": ["-race", "-timeout", "3s", "-cpu", "4", "-requiredcoverage", "100", "./..."]
      }
    }
  }
}
`
