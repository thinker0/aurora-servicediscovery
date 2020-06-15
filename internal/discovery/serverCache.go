package discovery

import (
	"errors"
	"github.com/patrickmn/go-cache"
	"github.com/prometheus/common/log"
	LOG "github.com/sirupsen/logrus"
	"github.com/thinker0/go-serversets"
	"regexp"
	"strings"
	"time"
)

type (
	ServiceSetsCache struct {
	hosts         []string
	pathServerSet *cache.Cache
	pathWatcher   *cache.Cache
	serviceSets   *cache.Cache
	match         *regexp.Regexp
}
)

func New(hosts []string) *ServiceSetsCache {
	c := new(ServiceSetsCache)

	c.hosts = hosts
	c.pathServerSet = cache.New(60*time.Minute, 10*time.Minute)
	c.pathWatcher  = cache.New(60*time.Minute, 10*time.Minute)
	c.serviceSets = cache.New(60*time.Minute, 10*time.Minute)
	c.match = regexp.MustCompile(`^[a-zA-Z0-9-_]+/(prod|release|releasedr|staging|devel|beta|alpha)/[a-zA-Z0-9-_]+$`)
	c.pathServerSet.OnEvicted(func(k string, v interface{}) {
		// set :=  v.(*serversets.ServerSet)
		log.Infof("ServerSet Evicted: %s", k)
	})
	c.pathWatcher.OnEvicted(func(k string, v interface{}) {
		watcher :=  v.(*serversets.Watch)
		watcher.Close()
		log.Infof("Watcher Evicted: %s", k)
	})
	c.serviceSets.OnEvicted(func(k string, v interface{}) {
		// set :=  v.(*serversets.ServerSet)
		log.Infof("Entity Evicted: %s", k)
	})
	LOG.Infof("Initialize ServiceSetsCache %s", c.hosts)
	return c
}

func (c *ServiceSetsCache) GetServerSetEntity(servicePath string) ([]serversets.Entity, error) {
	var serverSets *serversets.ServerSet = nil
	var serviceWatch *serversets.Watch = nil

	if len(servicePath) == 0 || !c.match.MatchString(servicePath) {
		LOG.Errorf("Invalid Path: {}", servicePath)
		return nil, errors.New("Service '" + servicePath + "', Not Match ")
	}
	if s, found := c.pathServerSet.Get(servicePath); found {
		serverSets = s.(*serversets.ServerSet)
	} else {
		paths := strings.Split(servicePath, "/")
		LOG.Infof("Initialize ServiceSets: %s/%s", c.hosts, servicePath)
		serverSets = serversets.New(paths[0], paths[1], paths[2], c.hosts)
		if serverSets == nil {
			return nil, errors.New("serverSets nil")
		}
		c.pathServerSet.Set(servicePath, serverSets, 60*time.Minute)
	}
	if w, found := c.pathWatcher.Get(servicePath); found {
		serviceWatch = w.(*serversets.Watch)
	} else {
		if serverSets == nil {
			return nil, errors.New("serverSets nil")
		}
		LOG.Infof("Initialize ServiceSets.Watch: %s/%s", c.hosts, servicePath)
		newServiceWatch, err := serverSets.Watch()
		if err != nil || newServiceWatch == nil {
			return nil, err
		}
		serviceWatch = newServiceWatch
		c.pathWatcher.Set(servicePath, newServiceWatch, 60*time.Minute)
	}
	if serviceWatch == nil {
		return nil, errors.New("serviceWatch nil")
	}
	LOG.Infof("Endpoints: %v", serviceWatch.Endpoints())
	return serviceWatch.EndpointEntities(), nil
}