package data

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path"
	"sync"
	"time"

	"github.com/NHAS/wag/pkg/queue"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"
)

type EventType int

func (ev EventType) String() string {
	switch ev {
	case CREATED:
		return "created"
	case DELETED:
		return "deleted"
	case MODIFIED:
		return "modified"
	}

	return "unknown event type"
}

const (
	CREATED = EventType(iota)
	DELETED
	MODIFIED
)

const (
	DevicesPrefix         = "devices-"
	UsersPrefix           = "users-"
	GroupMembershipPrefix = MembershipKey + "-"
	AclsPrefix            = "wag-acls-"
	GroupsPrefix          = "wag-groups-"
	ConfigPrefix          = "wag-config-"
	AuthenticationPrefix  = "wag-config-authentication-"
	NodeEvents            = "wag/node/"
	NodeErrors            = "wag/node/errors"
)

var (
	lck         sync.RWMutex
	contextMaps = map[string]context.CancelFunc{}

	clusterHealthLck       sync.RWMutex
	clusterHealthListeners = map[string]func(string){}

	EventsQueue = queue.NewQueue(40)
	exit        = make(chan bool)
)

func RegisterEventListener[T any](path string, isPrefix bool, f func(key string, current, previous T, et EventType) error) (string, error) {

	options := []clientv3.OpOption{
		clientv3.WithPrevKV(),
	}

	if isPrefix {
		options = append(options, clientv3.WithPrefix())
	}

	key, err := generateRandomBytes(16)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithCancel(context.Background())
	lck.Lock()
	contextMaps[key] = cancel
	lck.Unlock()

	wc := etcd.Watch(ctx, path, options...)
	go func(wc clientv3.WatchChan) {
		defer cancel()
		for watchEvent := range wc {
			for _, event := range watchEvent.Events {

				var (
					value = event.Kv.Value
					state EventType
				)
				if event.Type == clientv3.EventTypeDelete {
					state = DELETED
					value = event.PrevKv.Value
				} else if event.PrevKv == nil {
					state = CREATED
				} else {
					state = MODIFIED
				}

				var currentValue, previousValue T
				err := json.Unmarshal(value, &currentValue)
				if err != nil {
					log.Println("unable to unmarshal current type: ", path, string(value), err)
					continue
				}

				if event.PrevKv != nil {
					err = json.Unmarshal(event.PrevKv.Value, &previousValue)
					if err != nil {
						log.Println("unable to unmarshal previous type: ", err)
						continue
					}
				}

				go func(key []byte, prevKv *mvccpb.KeyValue) {
					if err := f(string(key), currentValue, previousValue, state); err != nil {
						log.Println("applying event failed: ", state, currentValue, "err:", err)
						err = RaiseError(err, value)
						if err != nil {
							log.Println("failed to raise error with cluster: ", err)
							return
						}
						return
					}

					switch state {
					case DELETED, CREATED:
						EventsQueue.Write([]byte(fmt.Sprintf("%s[%s]", key, state)))

					case MODIFIED:

						previous := "nil"
						if prevKv != nil {
							previous = string(prevKv.Value)
						}

						EventsQueue.Write([]byte(fmt.Sprintf("%s[%s]: %s -> %s", key, state, previous, string(value))))

					}

				}(event.Kv.Key, event.PrevKv)

			}
		}
	}(wc)

	return key, nil
}

func DeregisterEventListener(key string) {
	lck.Lock()
	defer lck.Unlock()

	if cancel, ok := contextMaps[key]; ok {
		if cancel != nil {
			cancel()
		}
		delete(contextMaps, key)
	}
}

func RegisterClusterHealthListener(f func(status string)) (string, error) {
	clusterHealthLck.Lock()
	defer clusterHealthLck.Unlock()

	key, err := generateRandomBytes(16)
	if err != nil {
		return "", err
	}

	clusterHealthListeners[key] = f

	return key, nil
}

func DeregisterClusterHealthListener(key string) {
	clusterHealthLck.Lock()
	defer clusterHealthLck.Unlock()

	delete(clusterHealthListeners, key)
}

func notifyClusterHealthListeners(event string) {
	clusterHealthLck.RLock()
	defer clusterHealthLck.RUnlock()

	for _, f := range clusterHealthListeners {
		go f(event)
	}
}

func checkClusterHealth() {

	leaderMonitor := time.NewTicker(1 * time.Second)
	go func() {
		for range leaderMonitor.C {
			if etcdServer.Server.Leader() == 0 {

				notifyClusterHealthListeners("electing")
				time.Sleep(etcdServer.Server.Cfg.ElectionTimeout() * 2)

				if etcdServer.Server.Leader() == 0 {
					notifyClusterHealthListeners("dead")
				}
			}
		}
	}()

	clusterMonitor := time.NewTicker(5 * time.Second)
	go func() {
		for range clusterMonitor.C {
			// If we're a learner we cant write to the cluster, so just wait until we're promoted
			if !etcdServer.Server.IsLearner() {
				testCluster()
			}
		}
	}()

	<-exit

	log.Println("etcd server was instructed to terminate")
	leaderMonitor.Stop()
	clusterMonitor.Stop()

}

func testCluster() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)

	_, err := etcd.Put(ctx, path.Join(NodeEvents, GetServerID(), "ping"), time.Now().Format(time.RFC1123Z))
	cancel()
	if err != nil {
		log.Println("unable to write liveness value")
		notifyClusterHealthListeners("dead")
		return
	}

	notifyHealthy()
}

func notifyHealthy() {
	if etcdServer.Server.IsLearner() {
		notifyClusterHealthListeners("learner")
	} else {
		notifyClusterHealthListeners("healthy")
	}
}

type EventError struct {
	NodeID          string
	ErrorID         string
	FailedEventData string
	Error           string
	Time            time.Time
}

func RaiseError(raisedError error, value []byte) (err error) {

	ee := EventError{
		NodeID:          GetServerID(),
		FailedEventData: string(value),
		Error:           raisedError.Error(),
		Time:            time.Now(),
	}

	ee.ErrorID, err = generateRandomBytes(16)
	if err != nil {
		return err
	}

	eventErrorBytes, _ := json.Marshal(ee)
	_, err = etcd.Put(context.Background(), path.Join(NodeErrors, ee.ErrorID), string(eventErrorBytes))

	return err

}

func GetAllErrors() (ret []EventError, err error) {
	response, err := etcd.Get(context.Background(), path.Join(NodeErrors), clientv3.WithPrefix(), clientv3.WithSort(clientv3.SortByKey, clientv3.SortDescend))
	if err != nil {
		return nil, err
	}

	for _, res := range response.Kvs {
		var ee EventError
		err := json.Unmarshal(res.Value, &ee)
		if err != nil {
			return nil, err
		}

		ret = append(ret, ee)
	}

	return ret, nil
}

func ResolveError(errorId string) error {
	_, err := etcd.Delete(context.Background(), path.Join(NodeErrors, errorId))
	return err
}
