//
//  Copyright 2026 The InfiniFlow Authors. All Rights Reserved.
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.
//

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"ragflow/psr/internal/ledger"
)

const maxInputSize = 16 << 20

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printUsage(stdout)
		return nil
	}
	switch args[0] {
	case "capture":
		return runCapture(args[1:], stdin, stdout, stderr)
	case "current":
		return runCurrent(args[1:], stdout, stderr)
	case "as-of":
		return runAsOf(args[1:], stdout, stderr)
	case "diff":
		return runDiff(args[1:], stdout, stderr)
	case "rollback":
		return runRollback(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown command %q; run psrtoc help", args[0])
	}
}

func runCapture(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags := newFlagSet("capture", stderr)
	storePath := flags.String("store", "", "local ledger JSON file")
	inputPath := flags.String("input", "-", "complete snapshot JSON file, or - for stdin")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := requireFlags(*storePath); err != nil {
		return err
	}

	reader := stdin
	if *inputPath != "-" {
		file, err := os.Open(*inputPath)
		if err != nil {
			return fmt.Errorf("open snapshot input: %w", err)
		}
		defer file.Close()
		reader = file
	}
	decoder := json.NewDecoder(io.LimitReader(reader, maxInputSize+1))
	decoder.DisallowUnknownFields()
	var snapshot ledger.Snapshot
	if err := decoder.Decode(&snapshot); err != nil {
		return fmt.Errorf("decode snapshot input: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("snapshot input contains more than one JSON value")
		}
		return fmt.Errorf("decode snapshot trailer: %w", err)
	}
	opened, err := ledger.Open(*storePath)
	if err != nil {
		return err
	}
	receipt, err := opened.Ingest(snapshot)
	if err != nil {
		return err
	}
	return writeJSON(stdout, receipt)
}

func runCurrent(args []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("current", stderr)
	storePath := flags.String("store", "", "local ledger JSON file")
	scope := flags.String("scope", "", "scope to read")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := requireFlags(*storePath, *scope); err != nil {
		return err
	}
	opened, err := ledger.Open(*storePath)
	if err != nil {
		return err
	}
	snapshot, err := opened.Current(*scope)
	if err != nil {
		return err
	}
	return writeJSON(stdout, snapshot)
}

func runAsOf(args []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("as-of", stderr)
	storePath := flags.String("store", "", "local ledger JSON file")
	scope := flags.String("scope", "", "scope to read")
	atValue := flags.String("time", "", "observation time in RFC3339 format")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := requireFlags(*storePath, *scope, *atValue); err != nil {
		return err
	}
	at, err := time.Parse(time.RFC3339, *atValue)
	if err != nil {
		return fmt.Errorf("parse --time as RFC3339: %w", err)
	}
	opened, err := ledger.Open(*storePath)
	if err != nil {
		return err
	}
	snapshot, err := opened.AsOfObservation(*scope, at)
	if err != nil {
		return err
	}
	return writeJSON(stdout, snapshot)
}

func runDiff(args []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("diff", stderr)
	storePath := flags.String("store", "", "local ledger JSON file")
	scope := flags.String("scope", "", "scope to compare")
	fromValue := flags.String("from", "", "starting observation time in RFC3339 format")
	toValue := flags.String("to", "", "ending observation time in RFC3339 format")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := requireFlags(*storePath, *scope, *fromValue, *toValue); err != nil {
		return err
	}
	from, err := time.Parse(time.RFC3339, *fromValue)
	if err != nil {
		return fmt.Errorf("parse --from as RFC3339: %w", err)
	}
	to, err := time.Parse(time.RFC3339, *toValue)
	if err != nil {
		return fmt.Errorf("parse --to as RFC3339: %w", err)
	}
	opened, err := ledger.Open(*storePath)
	if err != nil {
		return err
	}
	changes, err := opened.Diff(*scope, from, to)
	if err != nil {
		return err
	}
	return writeJSON(stdout, struct {
		Scope   string          `json:"scope"`
		From    time.Time       `json:"from"`
		To      time.Time       `json:"to"`
		Changes []ledger.Change `json:"changes"`
	}{Scope: *scope, From: from.UTC(), To: to.UTC(), Changes: changes})
}

func runRollback(args []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("rollback", stderr)
	storePath := flags.String("store", "", "local ledger JSON file")
	scope := flags.String("scope", "", "scope to roll back")
	toValue := flags.String("to", "", "historical observation time in RFC3339 format")
	atValue := flags.String("at", "", "new observation time in RFC3339 format")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := requireFlags(*storePath, *scope, *toValue, *atValue); err != nil {
		return err
	}
	to, err := time.Parse(time.RFC3339, *toValue)
	if err != nil {
		return fmt.Errorf("parse --to as RFC3339: %w", err)
	}
	at, err := time.Parse(time.RFC3339, *atValue)
	if err != nil {
		return fmt.Errorf("parse --at as RFC3339: %w", err)
	}
	opened, err := ledger.Open(*storePath)
	if err != nil {
		return err
	}
	historical, err := opened.AsOfObservation(*scope, to)
	if err != nil {
		return err
	}
	historical.ObservedAt = at
	receipt, err := opened.Ingest(historical)
	if err != nil {
		return fmt.Errorf("record rollback observation: %w", err)
	}
	return writeJSON(stdout, receipt)
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	return flags
}

func requireFlags(values ...string) error {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return errors.New("required flag is missing")
		}
	}
	return nil
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("write JSON output: %w", err)
	}
	return nil
}

func printUsage(writer io.Writer) {
	fmt.Fprintln(writer, `psrtoc stores complete, unverified local snapshots.

Commands:
  capture --store FILE [--input FILE|-]  Append a complete snapshot JSON value.
  current --store FILE --scope SCOPE    Read the latest observation.
  as-of --store FILE --scope SCOPE --time RFC3339
                                         Read the latest observation at or before time.
  diff --store FILE --scope SCOPE --from RFC3339 --to RFC3339
                                         Compare two observation times.
  rollback --store FILE --scope SCOPE --to RFC3339 --at RFC3339
                                         Re-record a historical observation as current.

The tool does not acquire or authenticate sources, verify claims, or connect to a
database. Keep source content local and use only data you are authorized to store.`)
}
