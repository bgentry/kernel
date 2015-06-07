package controllers

import (
	"fmt"
	"net/http"

	"github.com/convox/kernel/Godeps/_workspace/src/github.com/ddollar/logger"
	"github.com/convox/kernel/Godeps/_workspace/src/github.com/gorilla/mux"
	"github.com/convox/kernel/helpers"
	"github.com/convox/kernel/models"
)

func BuildCreate(rw http.ResponseWriter, r *http.Request) {
	log := buildsLogger("create").Start()

	vars := mux.Vars(r)
	app := vars["app"]
	repo := GetForm(r, "repo")

	build, err := models.NewBuild(app, "image")

	if err != nil {
		helpers.Error(log, err)
		RenderError(rw, err)
		return
	}

	err = build.Save()

	if err != nil {
		helpers.Error(log, err)
		RenderError(rw, err)
		return
	}

	log.Success("step=build.save app=%q", build.App)

	go build.Execute(repo)

	RenderText(rw, "ok")
}

func BuildPromote(rw http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	app := vars["app"]
	build := vars["build"]

	b, err := models.GetBuild(app, build)

	if err != nil {
		RenderError(rw, err)
		return
	}

	err = b.Promote()

	if err != nil {
		RenderError(rw, err)
		return
	}

	Redirect(rw, r, fmt.Sprintf("/apps/%s", app))
}

func buildsLogger(at string) *logger.Logger {
	return logger.New("ns=kernel cn=builds").At(at)
}
