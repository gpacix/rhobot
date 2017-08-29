package main

import (
	"os"
	"strconv"
	"strings"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/cfpb/rhobot/config"
	"github.com/cfpb/rhobot/database"
	"github.com/cfpb/rhobot/gocd"
	"github.com/cfpb/rhobot/healthcheck"
	"github.com/cfpb/rhobot/report"
	"github.com/davecgh/go-spew/spew"
	"github.com/urfave/cli"
)

func updateLogLevel(c *cli.Context, config *config.Config) {
	if c.GlobalString("loglevel") != "" {
		config.SetLogLevel(c.GlobalString("loglevel"))
	}
}

func updateGOCDHost(c *cli.Context, config *config.Config) (gocdServer *gocd.Server) {
	if c.String("host") != "" {
		config.SetGoCDHost(c.String("host"))
	}
	gocdServer = gocd.NewServerConfig(config.GOCDHost, config.GOCDPort, config.GOCDUser, config.GOCDPassword, config.GOCDTimeout)
	return
}

func healthcheckRunner(config *config.Config, healthcheckPath string, reportPath string, emailListPath string, hcSchema string, hcTable string) {
	healthChecks, err := healthcheck.ReadHealthCheckYAMLFromFile(healthcheckPath)
	if err != nil {
		log.Fatal("Failed to read healthchecks: ", err)
	}
	cxn := database.GetPGConnection(config.DBURI())

	results, HCerrs := healthChecks.PreformHealthChecks(cxn)
	numErrors := 0
	fatal := false
	for _, hcerr := range HCerrs {
		if strings.Contains(strings.ToUpper(hcerr.Err), "FATAL") {
			fatal = true
		}
		if strings.Contains(strings.ToUpper(hcerr.Err), "ERROR") {
			numErrors = numErrors + 1
		}
	}

	var elements []report.Element
	for _, val := range results {
		elements = append(elements, val)
	}

	// Make Templated report
	metadata := map[string]interface{}{
		"name":      healthChecks.Name,
		"db_name":   config.PgDatabase,
		"footer":    healthcheck.FooterHealthcheck,
		"timestamp": time.Now().Format(time.ANSIC),
		"status":    healthcheck.StatusHealthchecks(numErrors, fatal),
		"schema":    hcSchema,
		"table":     hcTable,
	}
	rs := report.Set{Elements: elements, Metadata: metadata}

	// Write report to file
	if reportPath != "" {
		prr := report.NewPongo2ReportRunnerFromString(healthcheck.TemplateHealthcheckHTML)
		reader, _ := prr.ReportReader(rs)
		fhr := report.FileHandler{Filename: reportPath}
		err = fhr.HandleReport(reader)
		if err != nil {
			log.Error("error writing report to PG database: ", err)
		}
	}

	// Email report
	if emailListPath != "" {
		prr := report.NewPongo2ReportRunnerFromString(healthcheck.TemplateHealthcheckHTML)
		df, err := report.ReadDistributionFormatYAMLFromFile(emailListPath)
		if err != nil {
			log.Fatal("Failed to read distribution format: ", err)
		}

		for _, level := range report.LogLevelArray {

			subjectStr := healthcheck.SubjectHealthcheck(healthChecks.Name, config.PgDatabase, config.PgHost, level, numErrors, fatal)

			logFilteredSet := report.FilterReportSet(rs, level)
			reader, _ := prr.ReportReader(logFilteredSet)
			recipients := df.GetEmails(level)

			if recipients != nil && len(recipients) != 0 && len(logFilteredSet.Elements) != 0 {
				log.Infof("Send %s to: %v", subjectStr, recipients)
				ehr := report.EmailHandler{
					SMTPHost:    config.SMTPHost,
					SMTPPort:    config.SMTPPort,
					SenderEmail: config.SMTPEmail,
					SenderName:  config.SMTPName,
					Subject:     subjectStr,
					Recipients:  recipients,
					HTML:        true,
				}
				err = ehr.HandleReport(reader)
				if err != nil {
					log.Error("Failed to email report: ", err)
				}
			}
		}
	}

	if hcSchema != "" && hcTable != "" {
		prr := report.NewPongo2ReportRunnerFromString(healthcheck.TemplateHealthcheckPostgres)
		pgr := report.PGHandler{Cxn: cxn}
		reader, err := prr.ReportReader(rs)
		err = pgr.HandleReport(reader)
		if err != nil {
			log.Errorf("Failed to save healthchecks to PG database\n%s", err)
		}
	}

	// Bad Exit
	if HCerrs != nil {
		log.Fatal("Healthchecks Failed:\n", spew.Sdump(HCerrs))
	}
}

func getArtifact(gocdServer *gocd.Server, pipeline string, stage string, job string,
	pipelineRun string, stageRun string, artifactPath string, artifactSavePath string) {

	//parse or get latest run numbers for pipeline and stage
	var pipelineRunNum, stageRunNum int = 0, 0
	var pipelineOk, stageOk bool = true, true
	var pipelineErr, stageErr error

	counterMap, err := gocd.History(gocdServer, pipeline)
	if err != nil {
		log.Fatalf("Could not find run history for pipeline: %v", pipeline)
	}
	log.Debug(spew.Sdump(counterMap))

	if pipelineRun == "0" {
		pipelineRunNum, pipelineOk = counterMap["p_"+pipeline]
	} else {
		pipelineRunNum, pipelineErr = strconv.Atoi(pipelineRun)
	}

	if stageRun == "0" {
		stageRunNum, stageOk = counterMap["s_"+stage]
	} else {
		stageRunNum, stageErr = strconv.Atoi(stageRun)
	}

	if !pipelineOk && !stageOk {
		log.Fatalf("Pipeline: \"%v\" and Stage: \"%v\" not found in pipeline history", pipeline, stage)
	}
	if pipelineErr != nil || stageErr != nil {
		log.Fatalf("Pipeline: %v and Stage: %v could not be parsed to integers", pipelineRun, stageRun)
	}

	//fetch artifact
	log.Infof("getting GoCD Artifact - pipeline:%v , pipelineRunNum:%v , stage:%v , stageRunNum:%v , job:%v , artifactPath:%v",
		pipeline, pipelineRunNum, stage, stageRunNum, job, artifactPath)
	artifactBuffer, err := gocd.Artifact(gocdServer, pipeline, pipelineRunNum, stage, stageRunNum, job, artifactPath)
	if err != nil {
		log.Fatalf("Failed to fetch artifact: %v", artifactPath)
	}

	//write to file or log
	if artifactSavePath == "" {
		artifactBuffer.WriteTo(os.Stdout)
	} else {
		f, err := os.Create(artifactSavePath)
		if err != nil {
			log.Fatalf("Failed to create file: %v", artifactSavePath)
		}

		_, err = artifactBuffer.WriteTo(f)
		if err != nil {
			log.Fatalf("Failed to write to file: %v", artifactSavePath)
		}
		defer f.Close()
	}

}
