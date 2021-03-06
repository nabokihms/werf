package docs

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/flant/werf/cmd/werf/common"
)

var CmdData struct {
	dest        string
	readmePath  string
	splitReadme bool
}
var CommonCmdData common.CmdData

func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "docs",
		DisableFlagsInUseLine: true,
		Short:                 "Generate documentation as markdown",
		Hidden:                true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := common.ProcessLogOptions(&CommonCmdData); err != nil {
				common.PrintHelp(cmd)
				return err
			}

			if CmdData.splitReadme {
				if err := SplitReadme(); err != nil {
					return err
				}
			} else {
				if err := GenMarkdownTree(cmd.Root(), CmdData.dest); err != nil {
					return err
				}
			}

			return nil
		},
	}

	common.SetupLogOptions(&CommonCmdData, cmd)

	f := cmd.Flags()
	f.StringVar(&CmdData.dest, "dir", "./", "directory to which documentation is written")
	f.StringVar(&CmdData.readmePath, "readme", "README.md", "path to README.md")
	f.BoolVar(&CmdData.splitReadme, "split-readme", false, "split README.md by top headers")

	return cmd
}

type SplitState string

const (
	noPartial     SplitState = "noDoc"
	insidePartial SplitState = "docFound"
)

const (
	docsPartialBeginLeft  = "<!-- WERF DOCS PARTIAL BEGIN: "
	docsPartialBeginRight = " -->"
	docsPartialEnd        = "<!-- WERF DOCS PARTIAL END -->"
)

func SplitReadme() error {
	file, err := os.Open(CmdData.readmePath)
	if err != nil {
		return err
	}

	defer file.Close()

	isPartialBegin := func(line string) bool {
		return strings.HasPrefix(line, docsPartialBeginLeft) && strings.HasSuffix(line, docsPartialBeginRight)
	}
	getPartialTitle := func(line string) string {
		res := strings.TrimPrefix(line, docsPartialBeginLeft)
		res = strings.TrimSuffix(res, docsPartialBeginRight)
		return res
	}
	isPartialEnd := func(line string) bool {
		return line == docsPartialEnd
	}

	state := noPartial
	currentPartialTitle := ""
	partialsData := map[string][]string{}

	scanner := bufio.NewScanner(file)
	scanner.Split(bufio.ScanLines)
	for scanner.Scan() {
		line := scanner.Text()

		switch state {
		case noPartial:
			if isPartialBegin(line) {
				currentPartialTitle = getPartialTitle(line)
				state = insidePartial
			}

		case insidePartial:
			if isPartialBegin(line) {
				currentPartialTitle = getPartialTitle(line)
				state = insidePartial
			} else if isPartialEnd(line) {
				state = noPartial
			} else {
				partialsData[currentPartialTitle] = append(partialsData[currentPartialTitle], line)
			}
		}
	}

	for header, data := range partialsData {
		basename := strings.ToLower(header)
		basename = strings.Replace(basename, " ", "_", -1)
		basename = strings.Replace(basename, "-", "_", -1)
		basename = basename + ".md"

		filename := filepath.Join(CmdData.dest, basename)
		f, err := os.Create(filename)
		if err != nil {
			return err
		}
		defer f.Close()

		preambleStr := "<!-- THIS FILE IS AUTOGENERATED BY werf docs COMMAND! DO NOT EDIT! -->\n\n"

		dataStr := strings.Join(data, "\n")
		dataStr = strings.TrimSpace(dataStr)

		partialDataStr := preambleStr + dataStr + "\n"

		if _, err := io.WriteString(f, partialDataStr); err != nil {
			return err
		}

		if err := f.Close(); err != nil {
			return err
		}
	}

	return nil
}
