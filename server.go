package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strconv"

	"github.com/GeertJohan/go.rice"
	"github.com/NYTimes/gziphandler"
	"github.com/julienschmidt/httprouter"
	"github.com/prologic/bitcask"
	"github.com/rcrowley/go-metrics"
	"github.com/rcrowley/go-metrics/exp"
	log "github.com/sirupsen/logrus"
	"github.com/thoas/stats"
	"github.com/unrolled/logger"
)

// Counters ...
type Counters struct {
	r metrics.Registry
}

func NewCounters() *Counters {
	counters := &Counters{
		r: metrics.NewRegistry(),
	}
	return counters
}

func (c *Counters) Inc(name string) {
	metrics.GetOrRegisterCounter(name, c.r).Inc(1)
}

func (c *Counters) Dec(name string) {
	metrics.GetOrRegisterCounter(name, c.r).Dec(1)
}

func (c *Counters) IncBy(name string, n int64) {
	metrics.GetOrRegisterCounter(name, c.r).Inc(n)
}

func (c *Counters) DecBy(name string, n int64) {
	metrics.GetOrRegisterCounter(name, c.r).Dec(n)
}

// Server ...
type Server struct {
	bind      string
	templates *Templates
	router    *httprouter.Router

	// Logger
	logger *logger.Logger

	// Stats/Metrics
	counters *Counters
	stats    *stats.Stats
}

func (s *Server) render(name string, w http.ResponseWriter, ctx interface{}) {
	buf, err := s.templates.Exec(name, ctx)
	if err != nil {
		log.WithError(err).Error("error rending template")
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	_, err = buf.WriteTo(w)
	if err != nil {
		log.WithError(err).Error("error writing response")
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

type TemplateContext struct {
	TodoList []*Todo
}

// IndexHandler ...
func (s *Server) IndexHandler() httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
		s.counters.Inc("n_index")

		var todoList TodoList

		err := db.Fold(func(key string) error {
			if key == "nextid" {
				return nil
			}

			var todo Todo

			data, err := db.Get(key)
			if err != nil {
				log.WithError(err).WithField("key", key).Error("error getting todo")
				return err
			}

			err = json.Unmarshal(data, &todo)
			if err != nil {
				return err
			}
			todoList = append(todoList, &todo)
			return nil
		})
		if err != nil {
			log.WithError(err).Error("error listing todos")
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		sort.Sort(todoList)

		ctx := &TemplateContext{
			TodoList: todoList,
		}

		s.render("index", w, ctx)
	}
}

// AddHandler ...
func (s *Server) AddHandler() httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		s.counters.Inc("n_add")

		var nextID uint64
		rawNextID, err := db.Get("nextid")
		if err != nil {
			if err != bitcask.ErrKeyNotFound {
				log.WithError(err).Error("error getting nextid")
				http.Error(w, "Internal Error", http.StatusInternalServerError)
				return
			}
		} else {
			nextID = binary.BigEndian.Uint64(rawNextID)
		}

		todo := NewTodo(r.FormValue("title"))
		todo.ID = nextID

		data, err := json.Marshal(&todo)
		if err != nil {
			log.WithError(err).Error("error serializing todo")
			http.Error(w, "Internal Error", http.StatusInternalServerError)
			return
		}

		key := fmt.Sprintf("todo_%d", nextID)

		err = db.Put(key, data)
		if err != nil {
			log.WithError(err).Error("error storing todo")
			http.Error(w, "Internal Error", http.StatusInternalServerError)
			return
		}

		buf := make([]byte, 8)
		nextID++
		binary.BigEndian.PutUint64(buf, nextID)
		err = db.Put("nextid", buf)
		if err != nil {
			log.WithError(err).Error("error storing nextid")
			http.Error(w, "Internal Error", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, "/", http.StatusFound)
	}
}

// DoneHandler ...
func (s *Server) DoneHandler() httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		s.counters.Inc("n_done")

		var id string

		id = p.ByName("id")
		if id == "" {
			id = r.FormValue("id")
		}

		if id == "" {
			log.WithField("id", id).Warn("no id specified to mark as done")
			http.Error(w, "Internal Error", http.StatusInternalServerError)
			return
		}

		i, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			log.WithError(err).Error("error parsing id")
			http.Error(w, "Internal Error", http.StatusInternalServerError)
			return
		}

		var todo Todo

		key := fmt.Sprintf("todo_%d", i)
		data, err := db.Get(key)
		if err != nil {
			log.WithError(err).WithField("key", key).Error("error retriving todo")
			http.Error(w, "Internal Error", http.StatusInternalServerError)
			return
		}

		err = json.Unmarshal(data, &todo)
		if err != nil {
			log.WithError(err).WithField("key", key).Error("error unmarshaling todo")
			http.Error(w, "Internal Error", http.StatusInternalServerError)
			return
		}

		todo.ToggleDone()

		data, err = json.Marshal(&todo)
		if err != nil {
			log.WithError(err).WithField("key", key).Error("error marshaling todo")
			http.Error(w, "Internal Error", http.StatusInternalServerError)
			return
		}

		err = db.Put(key, data)
		if err != nil {
			log.WithError(err).WithField("key", key).Error("error storing todo")
			http.Error(w, "Internal Error", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, "/", http.StatusFound)
	}
}

// ClearHandler ...
func (s *Server) ClearHandler() httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		s.counters.Inc("n_clear")

		var id string

		id = p.ByName("id")
		if id == "" {
			id = r.FormValue("id")
		}

		if id == "" {
			log.WithField("id", id).Warn("no id specified to mark as done")
			http.Error(w, "Internal Error", http.StatusInternalServerError)
			return
		}

		i, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			log.WithError(err).Error("error parsing id")
			http.Error(w, "Internal Error", http.StatusInternalServerError)
			return
		}

		key := fmt.Sprintf("todo_%d", i)
		err = db.Delete(key)
		if err != nil {
			log.WithError(err).WithField("key", key).Error("error deleting todo")
			http.Error(w, "Internal Error", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, "/", http.StatusFound)
	}
}

// StatsHandler ...
func (s *Server) StatsHandler() httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		bs, err := json.Marshal(s.stats.Data())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		w.Write(bs)
	}
}

// ListenAndServe ...
func (s *Server) ListenAndServe() {
	log.Fatal(
		http.ListenAndServe(
			s.bind,
			s.logger.Handler(
				s.stats.Handler(
					gziphandler.GzipHandler(
						s.router,
					),
				),
			),
		),
	)
}

func (s *Server) initRoutes() {
	s.router.Handler("GET", "/debug/metrics", exp.ExpHandler(s.counters.r))
	s.router.GET("/debug/stats", s.StatsHandler())

	s.router.GET("/", s.IndexHandler())
	s.router.POST("/add", s.AddHandler())

	s.router.GET("/done/:id", s.DoneHandler())
	s.router.POST("/done/:id", s.DoneHandler())

	s.router.GET("/clear/:id", s.ClearHandler())
	s.router.POST("/clear/:id", s.ClearHandler())
}

// NewServer ...
func NewServer(bind string) *Server {
	server := &Server{
		bind:      bind,
		router:    httprouter.New(),
		templates: NewTemplates("base"),

		// Logger
		logger: logger.New(logger.Options{
			Prefix:               "todo",
			RemoteAddressHeaders: []string{"X-Forwarded-For"},
		}),

		// Stats/Metrics
		counters: NewCounters(),
		stats:    stats.New(),
	}

	// Templates
	box := rice.MustFindBox("templates")

	indexTemplate := template.New("index")
	template.Must(indexTemplate.Parse(box.MustString("index.html")))
	template.Must(indexTemplate.Parse(box.MustString("base.html")))

	server.templates.Add("index", indexTemplate)

	server.initRoutes()

	return server
}
