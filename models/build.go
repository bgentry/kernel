package models

import (
	"bufio"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/convox/kernel/Godeps/_workspace/src/github.com/awslabs/aws-sdk-go/aws"
	"github.com/convox/kernel/Godeps/_workspace/src/github.com/awslabs/aws-sdk-go/service/cloudformation"
	"github.com/convox/kernel/Godeps/_workspace/src/github.com/awslabs/aws-sdk-go/service/dynamodb"
	"github.com/convox/kernel/Godeps/_workspace/src/github.com/convox/env/crypt"
)

type Build struct {
	Id string

	App string

	Env      string
	Logs     string
	Manifest string
	Reason   string
	Status   string

	Started time.Time
	Ended   time.Time
}

type Builds []Build

func NewBuild(app, reason string) (*Build, error) {
	a, err := GetApp(app)

	if err != nil {
		return nil, err
	}

	build, err := a.LatestBuild()

	if err != nil {
		return nil, err
	}

	if build == nil {
		build = &Build{App: app}
	}

	build.Id = generateId("B", 10)

	build.Logs = ""
	build.Reason = reason
	build.Status = "created"
	build.Started = time.Now()
	build.Ended = time.Time{}

	return build, nil
}

func ListBuilds(app string) (Builds, error) {
	req := &dynamodb.QueryInput{
		KeyConditions: &map[string]*dynamodb.Condition{
			"app": &dynamodb.Condition{
				AttributeValueList: []*dynamodb.AttributeValue{&dynamodb.AttributeValue{S: aws.String(app)}},
				ComparisonOperator: aws.String("EQ"),
			},
		},
		IndexName:        aws.String("app.created"),
		Limit:            aws.Long(10),
		ScanIndexForward: aws.Boolean(false),
		TableName:        aws.String(buildsTable(app)),
	}

	res, err := DynamoDB().Query(req)

	if err != nil {
		return nil, err
	}

	builds := make(Builds, len(res.Items))

	for i, item := range res.Items {
		builds[i] = *buildFromItem(*item)
	}

	return builds, nil
}

func GetBuild(app, id string) (*Build, error) {
	if id == "" {
		return nil, fmt.Errorf("build id required")
	}

	req := &dynamodb.GetItemInput{
		ConsistentRead: aws.Boolean(true),
		Key: &map[string]*dynamodb.AttributeValue{
			"id": &dynamodb.AttributeValue{S: aws.String(id)},
		},
		TableName: aws.String(buildsTable(app)),
	}

	res, err := DynamoDB().GetItem(req)

	if err != nil {
		return nil, err
	}

	build := buildFromItem(*res.Item)

	return build, nil
}

func (b *Build) Save() error {
	app, err := GetApp(b.App)

	if err != nil {
		return err
	}

	if b.Id == "" {
		return fmt.Errorf("Id can not be blank")
	}

	if b.Started.IsZero() {
		b.Started = time.Now()
	}

	req := &dynamodb.PutItemInput{
		Item: &map[string]*dynamodb.AttributeValue{
			"id":      &dynamodb.AttributeValue{S: aws.String(b.Id)},
			"app":     &dynamodb.AttributeValue{S: aws.String(b.App)},
			"status":  &dynamodb.AttributeValue{S: aws.String(b.Status)},
			"created": &dynamodb.AttributeValue{S: aws.String(b.Started.Format(SortableTime))},
		},
		TableName: aws.String(buildsTable(b.App)),
	}

	if b.Env != "" {
		(*req.Item)["env"] = &dynamodb.AttributeValue{S: aws.String(b.Env)}
	}

	if b.Logs != "" {
		(*req.Item)["logs"] = &dynamodb.AttributeValue{S: aws.String(b.Logs)}
	}

	if b.Manifest != "" {
		(*req.Item)["manifest"] = &dynamodb.AttributeValue{S: aws.String(b.Manifest)}
	}

	if b.Reason != "" {
		(*req.Item)["reason"] = &dynamodb.AttributeValue{S: aws.String(b.Reason)}
	}

	if !b.Ended.IsZero() {
		(*req.Item)["ended"] = &dynamodb.AttributeValue{S: aws.String(b.Ended.Format(SortableTime))}
	}

	_, err = DynamoDB().PutItem(req)

	if err != nil {
		return err
	}

	env := []byte(b.Env)

	if app.Parameters["Key"] != "" {
		cr := crypt.New(os.Getenv("AWS_REGION"), os.Getenv("AWS_ACCESS"), os.Getenv("AWS_SECRET"))

		env, err = cr.Encrypt(app.Parameters["Key"], []byte(env))

		if err != nil {
			return err
		}
	}

	return s3Put(app.Outputs["Settings"], fmt.Sprintf("builds/%s/env", b.Id), env, true)
}

func (b *Build) Cleanup() error {
	// TODO: store Ami on build and clean up from here
	// and remove the ami cleanup in release.Cleanup()

	// app, err := GetApp(b.App)

	// if err != nil {
	//   return err
	// }

	// // delete ami
	// req := &ec2.DeregisterImageRequest{
	//   ImageID: aws.String(b.Ami),
	// }

	// return EC2.DeregisterImage(req)

	return nil
}

func (b *Build) Execute(repo string) {
	b.Status = "building"
	b.Save()

	name := b.App

	args := []string{"run", "-v", "/var/run/docker.sock:/var/run/docker.sock", "convox/build", "-id", b.Id, "-push", os.Getenv("REGISTRY_HOST"), "-auth", os.Getenv("REGISTRY_PASSWORD"), name}

	parts := strings.Split(repo, "#")

	if len(parts) > 1 {
		args = append(args, strings.Join(parts[0:len(parts)-1], "#"), parts[len(parts)-1])
		fmt.Printf("args %+v\n", args)
	} else {
		args = append(args, repo)
		fmt.Printf("args %+v\n", args)
	}

	cmd := exec.Command("docker", args...)

	stdout, err := cmd.StdoutPipe()
	cmd.Stderr = cmd.Stdout

	if err = cmd.Start(); err != nil {
		b.Fail(err)
		return
	}

	manifest := ""
	success := true
	scanner := bufio.NewScanner(stdout)

	for scanner.Scan() {
		parts := strings.SplitN(scanner.Text(), "|", 2)

		if len(parts) < 2 {
			b.Logs += fmt.Sprintf("%s\n", parts[0])
			continue
		}

		switch parts[0] {
		case "manifest":
			manifest += fmt.Sprintf("%s\n", parts[1])
		case "error":
			success = false
			fmt.Println(parts[1])
			b.Logs += fmt.Sprintf("%s\n", parts[1])
		default:
			fmt.Println(parts[1])
			b.Logs += fmt.Sprintf("%s\n", parts[1])
		}
	}

	err = cmd.Wait()

	if err != nil {
		b.Fail(err)
		return
	}

	if !success {
		b.Fail(fmt.Errorf("error from builder"))
		return
	}

	env, err := GetEnvironment(b.App)

	if err != nil {
		b.Fail(err)
		return
	}

	b.Env = env.Raw()
	b.Manifest = manifest
	b.Status = "complete"
	b.Ended = time.Now()
	b.Save()
}

func (b *Build) Fail(err error) {
	b.Status = "failed"
	b.Ended = time.Now()
	b.Logs += fmt.Sprintf("Build Error: %s\n", err)
	b.Save()
}

func (b *Build) EnvironmentUrl() string {
	app, err := GetApp(b.App)

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return ""
	}

	return fmt.Sprintf("https://%s.s3.amazonaws.com/builds/%s/env", app.Outputs["Settings"], b.Id)
}

func (b *Build) Formation() (string, error) {
	args := []string{"run", "-i", "convox/app", "-mode", "staging"}

	cmd := exec.Command("docker", args...)
	cmd.Stderr = os.Stderr

	in, err := cmd.StdinPipe()

	if err != nil {
		return "", err
	}

	out, err := cmd.StdoutPipe()

	if err != nil {
		return "", err
	}

	err = cmd.Start()

	if err != nil {
		return "", err
	}

	io.WriteString(in, b.Manifest)
	in.Close()

	data, err := ioutil.ReadAll(out)

	if err != nil {
		return "", err
	}

	err = cmd.Wait()

	if err != nil {
		return "", err
	}

	return string(data), nil
}

func (b *Build) Image(process string) string {
	return fmt.Sprintf("%s/%s-%s:%s", os.Getenv("REGISTRY"), b.App, process, b.Id)
}

func (b *Build) Processes() (Processes, error) {
	manifest, err := LoadManifest(b.Manifest)

	fmt.Printf("%+v\n", manifest)

	if err != nil {
		return nil, err
	}

	ps := manifest.Processes()

	ss, err := ListServices(b.App)

	for _, s := range ss {
		if s.Stack == "" {
			ps = append(ps, Process{
				App:   b.App,
				Name:  s.Name,
				Count: 1,
			})
		}
	}

	return ps, nil
}

func (b *Build) Promote() error {
	formation, err := b.Formation()

	if err != nil {
		return err
	}

	existing, err := formationParameters(formation)

	if err != nil {
		return err
	}

	app, err := GetApp(b.App)

	if err != nil {
		return err
	}

	pss, err := b.Processes()

	if err != nil {
		return err
	}

	for _, ps := range pss {
		app.Parameters[fmt.Sprintf("%sCommand", upperName(ps.Name))] = ps.Command
		app.Parameters[fmt.Sprintf("%sImage", upperName(ps.Name))] = fmt.Sprintf("%s/%s-%s:%s", os.Getenv("REGISTRY_HOST"), b.App, ps.Name, b.Id)
		app.Parameters[fmt.Sprintf("%sScale", upperName(ps.Name))] = strconv.Itoa(ps.Count)
	}

	app.Parameters["Environment"] = b.EnvironmentUrl()
	app.Parameters["Kernel"] = CustomTopic
	app.Parameters["Release"] = b.Id

	fmt.Printf("%+v\n%+v\n", pss, app.Parameters)

	params := []*cloudformation.Parameter{}

	for key, value := range app.Parameters {
		if _, ok := existing[key]; ok {
			fmt.Printf("key = %+v\n", key)
			fmt.Printf("value = %+v\n", value)
			params = append(params, &cloudformation.Parameter{ParameterKey: aws.String(key), ParameterValue: aws.String(value)})
		}
	}

	req := &cloudformation.UpdateStackInput{
		Capabilities: []*string{aws.String("CAPABILITY_IAM")},
		StackName:    aws.String(b.App),
		TemplateBody: aws.String(formation),
		Parameters:   params,
	}

	_, err = CloudFormation().UpdateStack(req)

	return err
}

func buildsTable(app string) string {
	return fmt.Sprintf("%s-builds", app)
}

func buildFromItem(item map[string]*dynamodb.AttributeValue) *Build {
	started, _ := time.Parse(SortableTime, coalesce(item["created"], ""))
	ended, _ := time.Parse(SortableTime, coalesce(item["ended"], ""))

	return &Build{
		Id:       coalesce(item["id"], ""),
		App:      coalesce(item["app"], ""),
		Env:      coalesce(item["env"], ""),
		Logs:     coalesce(item["logs"], ""),
		Manifest: coalesce(item["manifest"], ""),
		Reason:   coalesce(item["reason"], ""),
		Status:   coalesce(item["status"], ""),
		Started:  started,
		Ended:    ended,
	}
}
