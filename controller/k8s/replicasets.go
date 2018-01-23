package k8s

import (
	"fmt"
	"time"

	"k8s.io/api/core/v1"
	"k8s.io/api/extensions/v1beta1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

const replicaSetResource = "replicasets"

type ReplicaSetStore struct {
	store     *cache.Store
	reflector *cache.Reflector
	stopCh    chan struct{}
}

func NewReplicaSetStore(clientset *kubernetes.Clientset) (*ReplicaSetStore, error) {
	store := cache.NewStore(cache.MetaNamespaceKeyFunc)

	replicatSetListWatcher := cache.NewListWatchFromClient(
		clientset.ExtensionsV1beta1().RESTClient(),
		replicaSetResource,
		v1.NamespaceAll,
		fields.Everything(),
	)

	reflector := cache.NewReflector(
		replicatSetListWatcher,
		&v1beta1.ReplicaSet{},
		store,
		time.Duration(0),
	)

	stopCh := make(chan struct{})

	return &ReplicaSetStore{
		store:     &store,
		reflector: reflector,
		stopCh:    stopCh,
	}, nil
}

func (p *ReplicaSetStore) Run() error {
	go p.reflector.ListAndWatch(p.stopCh)
	return newWatcher(p.reflector, replicaSetResource).run()
}

func (p *ReplicaSetStore) Stop() {
	p.stopCh <- struct{}{}
}

func (p *ReplicaSetStore) GetReplicaSet(key string) (*v1beta1.ReplicaSet, error) {
	item, exists, err := (*p.store).GetByKey(key)

	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("no ReplicaSet exists for name %s", key)
	}
	rs, ok := item.(*v1beta1.ReplicaSet)
	if !ok {
		return nil, fmt.Errorf("%v is not a ReplicaSet", item)
	}
	return rs, nil
}

func (p *ReplicaSetStore) GetDeploymentForPod(pod *v1.Pod) (string, error) {
	namespace := pod.Namespace
	if len(pod.GetOwnerReferences()) == 0 {
		return "", fmt.Errorf("Pod %s has no owner", pod.Name)
	}
	parent := pod.GetOwnerReferences()[0]
	if parent.Kind == "ReplicaSet" {
		rsName := namespace + "/" + parent.Name
		rs, err := p.GetReplicaSet(rsName)
		if err != nil {
			return "", err
		}
		if len(rs.GetOwnerReferences()) == 0 {
			return namespace + "/" + rsName, nil
		}
		return namespace + "/" + rs.GetOwnerReferences()[0].Name, nil
	}
	return namespace + "/" + parent.Name, nil
}
