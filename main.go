package main

import (
  "context"
  "crypto/tls"
  "encoding/json"
  "fmt"
  "github.com/avast/retry-go"
  "github.com/bndr/gojenkins"
  "github.com/spf13/cobra"
  "net/http"
  "os"
  "strings"
  "time"
)

const (
  defaultJenkinsUrl      = "http://127.0.0.1:8080"
  defaultWait            = false
  defaultWaitPollSecond  = 10
  defaultWaitMaxAttempts = 60
  desc                   = `This command triggers Jenkins job.

You can specify the '--job'/'-j' flag to determine the name of the Jenkins job to run.
To passing job parameters, use either the '--params'/'-p' flag in key=value format,
can specify multiple or separate parameters with commas: foo=bar,baz=qux.
You can also use the '--params-json'/'-P' passing JSON format parameters from the command line.

  $ jenkins-trigger -j myjob
  $ jenkins-trigger -j myjob -p foo=bar -p baz=qux
  $ jenkins-trigger -j myjob -p foo=bar,baz=qux
  $ jenkins-trigger -j myjob -P '{"foo":"bar","baz":"qux"}'

You can specify the '--jenkins-url' flag to set the url of the Jenkins server,
and '--jenkins-user'/'--jenkins-pat' flag to set the user and personal access token (PAT)
if the Jenkins server requires auth to access.

  $ jenkins-trigger -j myjob --jenkins-url http://myjenkins.com:8080 --jenkins-user me --jenkins-pat mytoken

You can specify the '--wait' flag to waiting for the job complete, and return the results.
Use '--poll-time' flag (in duration format) to set how often to poll the jenkins server for results.
Use '--max-attempts' flag to set the max count of polling for results.

  $ jenkins-trigger -j myjob --wait
  $ jenkins-trigger -j myjob --wait --poll-time 10s --max-attempts 60
`
)

func main() {

  c := config{
    Jenkins: jenkins{
      Url: defaultJenkinsUrl,
    },
    Job: job{},
    Wait: wait{
      Enabled:     defaultWait,
      PollTime:    defaultWaitPollSecond * time.Second,
      MaxAttempts: defaultWaitMaxAttempts,
    },
  }

  params := params{}
  cmd := &cobra.Command{
    Use:          "jenkins-trigger",
    Short:        "Trigger Jenkins job in Go",
    Long:         desc,
    SilenceUsage: true,
    RunE: func(cmd *cobra.Command, args []string) (err error) {
      c.Job.Params, err = params.init()
      if err != nil {
        return
      }
      return triggerBuild(c)
    },
  }

  flags := cmd.Flags()
  flags.StringVar(&c.Jenkins.Url, "jenkins-url", c.Jenkins.Url, "URL of the Jenkins server")
  flags.StringVar(&c.Jenkins.User, "jenkins-user", c.Jenkins.User, "User for accessing Jenkins")
  flags.StringVar(&c.Jenkins.Pat, "jenkins-pat", c.Jenkins.Pat, "Personal access token (PAT) for accessing Jenkins")
  flags.BoolVarP(&c.Insecure, "insecure", "k", c.Insecure, "Allow insecure Jenkins server connections when using SSL")
  flags.StringVarP(&c.Job.Name, "job", "j", c.Job.Name, "The name of the Jenkins job to run")
  flags.StringSliceVarP(&params.slice, "params", "p", params.slice, "The parameters of the job in key=value format, can specify multiple or separate parameters with commas, e.g. foo=bar,baz=qux")
  flags.StringVarP(&params.json, "params-json", "P", params.json, "The parameters of the job in JSON format, e.g. {\"foo\":\"bar\",\"baz\":\"qux\"}")
  flags.BoolVar(&c.Wait.Enabled, "wait", c.Wait.Enabled, "Wait for the job to complete, and return the results")
  flags.DurationVar(&c.Wait.PollTime, "poll-time", c.Wait.PollTime, "How often (duration) to poll the Jenkins server for results")
  flags.UintVar(&c.Wait.MaxAttempts, "max-attempts", c.Wait.MaxAttempts, "Max count of polling for results")

  cmd.MarkFlagRequired("job")

  if err := cmd.Execute(); err != nil {
    fmt.Fprintln(os.Stderr, err)
    os.Exit(1)
  }
}

func createHttpClient(insecure bool) *http.Client {
  return &http.Client{Transport: &http.Transport{
    TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure},
  }}
}

func triggerBuild(c config) error {
  ctx := context.Background()

  fmt.Printf("Triggering Jenkins build for job: %+v, wait: %+v\n", c.Job, c.Wait)

  jenkins, err := gojenkins.CreateJenkins(createHttpClient(c.Insecure), c.Jenkins.Url, c.Jenkins.User, c.Jenkins.Pat).Init(ctx)
  if err != nil {
    return err
  }

  queueId, err := jenkins.BuildJob(ctx, c.Job.Name, c.Job.Params)
  if err != nil {
    return err
  }

  fmt.Printf("Job %s triggered successfully\n", c.Job.Name)

  if !c.Wait.Enabled {
    return nil
  }

  return retry.Do(
    pollBuildResult(c, jenkins, queueId),
    retry.Delay(c.Wait.PollTime),
    retry.Attempts(c.Wait.MaxAttempts),
  )
}

func pollBuildResult(c config, jenkins *gojenkins.Jenkins, queueId int64) func() error {
  return func() error {
    ctx := retry.DefaultContext
    fmt.Printf("Polling build result for job %s\n", c.Job.Name)

    build, err := jenkins.GetBuildFromQueueID(ctx, queueId)
    if err != nil {
      return err
    }

    if build.IsGood(ctx) {
      fmt.Printf("Job %s, build number %d successfully\n", c.Job.Name, build.GetBuildNumber())
      return nil
    }

    if build.IsRunning(ctx) {
      fmt.Printf("Job %s, build number %d is still running, retry after %s\n", c.Job.Name, build.GetBuildNumber(), c.Wait.PollTime)
      return &IsStillRunning{time.Now(), c.Job.Name, build.GetBuildNumber()}
    }

    return retry.Unrecoverable(fmt.Errorf("Job %s Build number %d did not complete successfully\n", c.Job.Name, build.GetBuildNumber()))
  }
}

type IsStillRunning struct {
  time        time.Time
  jobName     string
  buildNumber int64
}

func (r *IsStillRunning) Error() string {
  return fmt.Sprintf("job %s, build number %d is still running. (%s)\n", r.jobName, r.buildNumber, r.time.Format(time.Stamp))
}

type config struct {
  Jenkins  jenkins
  Job      job
  Wait     wait
  Insecure bool
}

type wait struct {
  Enabled     bool
  PollTime    time.Duration
  MaxAttempts uint
}

type jenkins struct {
  Url  string
  User string
  Pat  string
}

type job struct {
  Name   string
  Params map[string]string
}

type params struct {
  slice []string
  json  string
}

func (p *params) init() (map[string]string, error) {
  params := make(map[string]string)
  if p.json != "" {
    if err := json.Unmarshal([]byte(p.json), &params); err != nil {
      return nil, err
    }
  }
  for _, v := range p.slice {
    split := strings.Split(v, "=")
    params[strings.TrimSpace(split[0])] = strings.TrimSpace(strings.Join(split[1:], "="))
  }
  return params, nil
}
