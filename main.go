package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/alecthomas/kingpin"
	nested "github.com/antonfisher/nested-logrus-formatter"
	"github.com/sbreitf1/exec"
	"github.com/sbreitf1/fs"
	log "github.com/sirupsen/logrus"
)

var (
	appMain      = kingpin.New("gather-dependencies", "Copy all shared dependencies into a single directory")
	argInputFile = appMain.Arg("in", "Input binary file").Required().String()
	argOutputDir = appMain.Arg("out", "Output directory").Required().String()
)

func main() {
	log.SetFormatter(&nested.Formatter{
		HideKeys:        true,
		TimestampFormat: "15:04:05",
		NoColors:        true,
		TrimMessages:    true,
	})

	kingpin.MustParse(appMain.Parse(os.Args[1:]))

	inputFile, err := filepath.Abs(*argInputFile)
	if err != nil {
		log.Fatalf("Invalid input file: %v", err)
	}
	outputDir, err := filepath.Abs(*argOutputDir)
	if err != nil {
		log.Fatalf("Invalid output dir: %v", err)
	}
	log.Infof("Input file %q", inputFile)
	log.Infof("Output dir %q", *argOutputDir)

	// also useful: "readelf -d {INPUTFILE}"
	result, code, err := exec.Run("ldd", inputFile)
	if err != nil {
		log.Fatalf("Could not execute 'ldd': %v", err)
	}
	if code != 0 {
		log.Info(result)
		log.Fatalf("Code %d returned by 'ldd'", code)
	}

	pattern := regexp.MustCompile(`\s([a-z0-9.\-/]+)(\s+=>\s+([a-z0-9.\-_/]+))?\s+\(0x[0-9a-f]+\)`)
	matches := pattern.FindAllStringSubmatch(result, -1)

	log.Infof("Found %d dependencies", len(matches))
	count := 0
	for _, m := range matches {
		var depFile string
		if len(m[3]) == 0 {
			depFile = m[1]
		} else {
			depFile = m[3]
		}

		if !strings.HasPrefix(depFile, "/") {
			log.Infof("Ignore file %q", depFile)
		} else {
			log.Infof("Gather file %q", depFile)

			dstFile := filepath.Join(outputDir, depFile)
			dstDir := filepath.Dir(dstFile)
			if err := os.MkdirAll(dstDir, os.ModePerm); err != nil {
				log.Fatalf("-> Could not create parent directory: %v", err)
			}

			if err := fs.CopyFile(depFile, dstFile); err != nil {
				log.Fatalf("-> Failed to copy file: %v", err)
			}

			count++
		}
	}

	log.Infof("%d dependencies have been gathered", count)
}
