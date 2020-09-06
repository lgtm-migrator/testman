package main

import (
	"context"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/peterbourgon/ff/v3/ffcli"
	"moul.io/godev"
	"moul.io/motd"
)

var opts Opts

func main() {
	if err := run(os.Args); err != nil {
		if err != flag.ErrHelp {
			log.Fatalf("error: %v", err)
		}
		os.Exit(1)
	}
}

func run(args []string) error {
	// flags
	testFlags := flag.NewFlagSet("testman test", flag.ExitOnError)
	testFlags.BoolVar(&opts.Verbose, "v", false, "verbose")
	testFlags.StringVar(&opts.Run, "run", "^(Test|Example)", "regex to filter out tests and examples")
	//testFlags.IntVar(&opts.Retry, "retry", 0, "fail after N retries")
	//testFlags.DurationVar(&opts.Timeout, "timeout", opts.Timeout, "max duration allowed to run the whole suite")
	listFlags := flag.NewFlagSet("testman list", flag.ExitOnError)
	listFlags.BoolVar(&opts.Verbose, "v", false, "verbose")
	listFlags.StringVar(&opts.Run, "run", "^(Test|Example)", "regex to filter out tests and examples")

	root := &ffcli.Command{
		ShortUsage: "testman <subcommand> [flags]",
		ShortHelp:  "Advanced testing workflows for Go projects.",
		Exec: func(ctx context.Context, args []string) error {
			fmt.Println(motd.Default())
			return flag.ErrHelp
		},
		Subcommands: []*ffcli.Command{
			{
				Name:       "test",
				FlagSet:    testFlags,
				ShortHelp:  "advanced go test workflows",
				ShortUsage: "testman test [flags] [packages]",
				LongHelp:   "EXAMPLES\n   testman test -v ./...",
				Exec:       runTest,
			}, {
				Name:       "list",
				FlagSet:    listFlags,
				ShortHelp:  "list available tests",
				ShortUsage: "testman list [packages]",
				LongHelp:   "EXAMPLE\n   testman list ./...",
				Exec:       runList,
			},
		},
	}

	return root.ParseAndRun(context.Background(), args[1:])
}

func runList(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return flag.ErrHelp
	}
	preRun()

	// list packages
	pkgs, err := listPackagesWithTests(args)
	if err != nil {
		return err
	}

	// list tests
	for _, pkg := range pkgs {
		tests, err := listDirTests(pkg.Dir)
		if err != nil {
			return err
		}
		if len(tests) == 0 {
			continue
		}

		fmt.Println(pkg.ImportPath)
		for _, test := range tests {
			fmt.Printf("  %s\n", test)
		}
	}
	return nil
}

func runTest(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return flag.ErrHelp
	}
	preRun()
	log.Printf("runTest opts=%s args=%s", godev.JSON(opts), godev.JSON(args))
	start := time.Now()

	// list packages
	pkgs, err := listPackagesWithTests(args)
	if err != nil {
		return err
	}

	// create temp dir
	tmpdir, err := ioutil.TempDir("", "testman")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpdir)

	atLeastOneFailure := false
	// list tests
	for _, pkg := range pkgs {
		tests, err := listDirTests(pkg.Dir)
		if err != nil {
			return err
		}
		if len(tests) == 0 {
			continue
		}

		pkgStart := time.Now()
		// compile test binary
		bin, err := compileTestBin(pkg, tmpdir)
		if err != nil {
			fmt.Printf("FAIL\t%s\t[compile error: %v]\n", pkg.ImportPath, err)
			return err
		}

		isPackageOK := true
		for _, test := range tests {
			// FIXME: check if matches run regex
			args := []string{
				"-test.count=1",
				"-test.timeout=300s",
			}
			if opts.Verbose {
				args = append(args, "-test.v")
			}
			args = append(args, "-test.run", fmt.Sprintf("^%s$", test))
			cmd := exec.Command(bin, args...)
			log.Println(cmd.String())
			out, err := cmd.CombinedOutput()
			if err != nil {
				fmt.Printf("FAIL\t%s.%s\t[compile error: %v]\n", pkg.ImportPath, test, err)
				if opts.Verbose {
					fmt.Println(string(out))
				}
				isPackageOK = false
				atLeastOneFailure = true
			}
		}
		if isPackageOK {
			fmt.Printf("ok\t%s\t%s\n", pkg.ImportPath, time.Since(pkgStart))
		}
	}

	fmt.Printf("total: %s\n", time.Since(start))
	if atLeastOneFailure {
		os.Exit(1)
	}
	return nil
}

func preRun() {
	if !opts.Verbose {
		log.SetOutput(ioutil.Discard)
	}
}

func compileTestBin(pkg Package, tempdir string) (string, error) {
	name := strings.Replace(pkg.ImportPath, "/", "~", -1)
	bin := filepath.Join(tempdir, name)
	cmd := exec.Command("go", "test", "-c", "-o", bin)
	cmd.Dir = pkg.Dir
	log.Println(cmd.String())
	out, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Fprintln(os.Stderr, string(out))
		return "", err
	}

	return bin, nil
}

func listDirTests(dir string) ([]string, error) {
	cmd := exec.Command("go", "test", "-list", ".")
	cmd.Dir = dir
	log.Println(cmd.String())
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(out)) == "" {
		return nil, nil
	}
	tests := []string{}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "ok ") {
			continue
		}
		if opts.Run != "" {
			matched, err := regexp.MatchString(opts.Run, line)
			if err != nil {
				return nil, err
			}
			if !matched {
				continue
			}
		}
		tests = append(tests, line)
	}
	return tests, nil
}

func listPackagesWithTests(patterns []string) ([]Package, error) {
	cmdArgs := append([]string{"list", "-test", "-f", "{{.ImportPath}} {{.Dir}}"}, patterns...)
	cmd := exec.Command("go", cmdArgs...)
	log.Println(cmd.String())
	out, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Fprintln(os.Stderr, string(out))
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	pkgs := []Package{}
	for _, line := range lines {
		parts := strings.SplitN(line, " ", 2)
		if !strings.HasSuffix(parts[0], ".test") {
			continue
		}
		pkgs = append(pkgs, Package{
			ImportPath: strings.TrimSuffix(parts[0], ".test"),
			Dir:        parts[1],
		})
	}
	return pkgs, nil
}

type Package struct {
	Dir        string
	ImportPath string
}

type Opts struct {
	Verbose bool
	Run     string
	// Timeout time.Duration
	// Retry   int
	// c
	// debug
	// continueOnFailure vs failFast
}
