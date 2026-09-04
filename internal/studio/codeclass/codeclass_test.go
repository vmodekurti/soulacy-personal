package codeclass

import (
	"reflect"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name     string
		code     string
		requires []string
		dynamic  bool
	}{
		{"pure data is readonly", "import json, re\ndef run(inputs):\n    return {k: v for k, v in inputs.items()}", nil, false},
		{"subprocess is system", "import subprocess\ndef run(i):\n    return subprocess.run(['ls']).returncode", []string{"system"}, false},
		{"requests is network", "import requests\ndef run(i):\n    return requests.get(i['url']).text", []string{"network"}, false},
		{"both", "import subprocess, httpx\ndef run(i):\n    httpx.get('x'); subprocess.run(['nlm'])", []string{"network", "system"}, false},
		{"file write is system", "def run(i):\n    open('/tmp/x','w').write('hi')", []string{"system"}, false},
		{"eval is dynamic", "def run(i):\n    return eval(i['expr'])", nil, true},
		{"os.system is system", "import os\ndef run(i):\n    os.system('echo hi')", []string{"system"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Classify(c.code)
			if !reflect.DeepEqual(got.Requires, c.requires) {
				t.Fatalf("Requires = %v, want %v", got.Requires, c.requires)
			}
			if got.Dynamic != c.dynamic {
				t.Fatalf("Dynamic = %v, want %v", got.Dynamic, c.dynamic)
			}
		})
	}
}

func TestBeyond(t *testing.T) {
	if Classify("import json").Beyond() {
		t.Fatal("pure data must be inside guardrails")
	}
	if !Classify("import subprocess").Beyond() {
		t.Fatal("subprocess must be beyond guardrails")
	}
	if !Classify("eval(x)").Beyond() {
		t.Fatal("dynamic must be beyond guardrails")
	}
}
