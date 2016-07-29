package main

import (
	"testing"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/cfpb/rhobot/config"
	"github.com/cfpb/rhobot/database"
	"github.com/cfpb/rhobot/healthcheck"
	"github.com/cfpb/rhobot/report"
)

var conf *config.Config

func init() {
	conf = config.NewConfig()
	conf.SetLogLevel("info")
}

func TestPostgresHealthCheckReporting(t *testing.T) {
	cxn := database.GetPGConnection(conf.DBURI())
	healthChecks, _ := healthcheck.ReadHealthCheckYAMLFromFile("healthcheck/healthchecksAll.yml")
	results, _ := healthChecks.PreformHealthChecks(cxn)
	var elements []report.Element
	for _, val := range results {
		elements = append(elements, val)
	}
	metadata := map[string]interface{}{
		"name":      healthChecks.Name,
		"schema":    "public",
		"table":     "healthchecks",
		"timestamp": time.Now().Format(time.ANSIC),
	}
	rs := report.Set{Elements: elements, Metadata: metadata}

	prr := report.NewPongo2ReportRunnerFromString(healthcheck.TemplateHealthcheckPostgres)
	pgr := report.PGHandler{Cxn: cxn}
	reader, err := prr.ReportReader(rs)
	err = pgr.HandleReport(reader)
	if err != nil {
		t.Fatalf("error writing report to PG database\n%s", err)
	}
}

func TestLogLevelingFiltering(t *testing.T) {
	cxn := database.GetPGConnection(conf.DBURI())
	healthChecks, _ := healthcheck.ReadHealthCheckYAMLFromFile("healthcheck/healthchecksAll.yml")
	results, _ := healthChecks.PreformHealthChecks(cxn)
	var elements []report.Element
	for _, val := range results {
		elements = append(elements, val)
	}
	metadata := map[string]interface{}{
		"name": healthChecks.Name,
	}
	originalReportSet := report.Set{Elements: elements, Metadata: metadata}
	TestLogLevelResults := map[string]int{
		"Debug": 6,
		"Info":  5,
		"Warn":  4,
		"Error": 2,
		"Fatal": 1,
	}

	for _, level := range report.LogLevelArray {
		log.Infof("Report Filter LogLevel %v", level)
		logFilteredSet := report.FilterReportSet(originalReportSet, level)
		prr := report.JSONReportRunner{}
		reader, _ := prr.ReportReader(logFilteredSet)
		phr := report.PrintHandler{}
		_ = phr.HandleReport(reader)

		if len(logFilteredSet.GetElementArray()) != TestLogLevelResults[level] {
			t.Fatalf("wrong number of healthchecks in report")
		}
	}

}
