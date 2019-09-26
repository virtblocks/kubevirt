package virtblocks

import (
	"fmt"
	"path/filepath"
	"time"

	"golang.org/x/net/context"
	"google.golang.org/grpc"

	k8sv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/reference"

	v1 "kubevirt.io/client-go/api/v1"
	"kubevirt.io/client-go/log"
	com "kubevirt.io/kubevirt/pkg/handler-launcher-com"
	"kubevirt.io/kubevirt/pkg/handler-launcher-com/notify/info"
	notifyv1 "kubevirt.io/kubevirt/pkg/handler-launcher-com/notify/v1"
	grpcutil "kubevirt.io/kubevirt/pkg/util/net/grpc"
	"kubevirt.io/kubevirt/pkg/virt-launcher/virtwrap/api"
	"kubevirt.io/kubevirt/pkg/virt-launcher/virtwrap/util"
)

var (
	// add older version when supported
	// don't use the variable in pkg/handler-launcher-com/notify/v1/version.go in order to detect version mismatches early
	supportedNotifyVersions = []uint32{1}
)

type NotifierImpl struct {
	v1client notifyv1.NotifyClient
	conn     *grpc.ClientConn
}

type libvirtEvent struct {
	Domain string
}

func NewNotifier(virtShareDir string) (Notifier, error) {
	// dial socket
	socketPath := filepath.Join(virtShareDir, "domain-notify.sock")
	conn, err := grpcutil.DialSocket(socketPath)
	if err != nil {
		log.Log.Reason(err).Infof("failed to dial notify socket: %s", socketPath)
		return nil, err
	}

	// create info v1client and find cmd version to use
	infoClient := info.NewNotifyInfoClient(conn)
	return NewNotifierWithInfoClient(infoClient, conn)

}

func NewNotifierWithInfoClient(infoClient info.NotifyInfoClient, conn *grpc.ClientConn) (*NotifierImpl, error) {

	ctx, _ := context.WithTimeout(context.Background(), 5*time.Second)
	info, err := infoClient.Info(ctx, &info.NotifyInfoRequest{})
	if err != nil {
		return nil, fmt.Errorf("could not check cmd server version: %v", err)
	}
	version, err := com.GetHighestCompatibleVersion(info.SupportedNotifyVersions, supportedNotifyVersions)
	if err != nil {
		return nil, err
	}

	// create cmd v1client
	switch version {
	case 1:
		client := notifyv1.NewNotifyClient(conn)
		return newV1Notifier(client, conn), nil
	default:
		return nil, fmt.Errorf("cmd v1client version %v not implemented yet", version)
	}

}

func newV1Notifier(client notifyv1.NotifyClient, conn *grpc.ClientConn) *NotifierImpl {
	return &NotifierImpl{
		v1client: client,
		conn:     conn,
	}
}

func (n *NotifierImpl) SendDomainEvent(event watch.Event) error {

	var domainJSON []byte
	var statusJSON []byte
	var err error

	if event.Type == watch.Error {
		status := event.Object.(*metav1.Status)
		statusJSON, err = json.Marshal(status)
		if err != nil {
			return err
		}
	} else {
		domain := event.Object.(*api.Domain)
		domainJSON, err = json.Marshal(domain)
		if err != nil {
			return err
		}
	}
	request := notifyv1.DomainEventRequest{
		DomainJSON: domainJSON,
		StatusJSON: statusJSON,
		EventType:  string(event.Type),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	response, err := n.v1client.HandleDomainEvent(ctx, &request)

	if err != nil {
		return err
	} else if response.Success != true {
		msg := fmt.Sprintf("failed to notify domain event: %s", response.Message)
		return fmt.Errorf(msg)
	}

	return nil
}

func newWatchEventError(err error) watch.Event {
	return watch.Event{Type: watch.Error, Object: &metav1.Status{Status: metav1.StatusFailure, Message: err.Error()}}
}

func eventCallback(c *VirtBlocksImpl, domain *api.Domain, client *NotifierImpl, events chan watch.Event, interfaceStatus *[]api.InterfaceStatus) {
	d, err := c.GetDomain()
	if err != nil {
		if !isNotFound(err) {
			log.Log.Reason(err).Error("Could not fetch the Domain.")
			client.SendDomainEvent(newWatchEventError(err))
			return
		}
		domain.SetState(api.NoState, api.ReasonNonExistent)
	} else {
		// No matter which event, try to fetch the domain xml
		// and the state. If we get a IsNotFound error, that
		// means that the VirtualMachineInstance was removed.
		status, reason, err := d.GetState()
		if err != nil {
			if !isNotFound(err) {
				log.Log.Reason(err).Error("Could not fetch the Domain state.")
				client.SendDomainEvent(newWatchEventError(err))
				return
			}
			domain.SetState(api.NoState, api.ReasonNonExistent)
		} else {
			domain.SetState(status, reason)
		}

		// XXX metadata is missing here
		domain, err := d.Spec()
		if err != nil {
			// NOTE: Getting domain metadata for a live-migrating VM isn't allowed
			if !isNotFound(err) {
				log.Log.Reason(err).Error("Could not fetch the Domain specification.")
				client.SendDomainEvent(newWatchEventError(err))
				return
			}
		} else {
			domain.ObjectMeta.UID = domain.Spec.Metadata.KubeVirt.UID
		}
		log.Log.Infof("kubevirt domain status: %v(%v):%v(%v)", domain.Status.Status, status, domain.Status.Reason, reason)
	}

	switch domain.Status.Reason {
	case api.ReasonNonExistent:
		watchEvent := watch.Event{Type: watch.Deleted, Object: domain}
		client.SendDomainEvent(watchEvent)
		events <- watchEvent
	default:
		if interfaceStatus != nil {
			domain.Status.Interfaces = *interfaceStatus
			event := watch.Event{Type: watch.Modified, Object: domain}
			client.SendDomainEvent(event)
			events <- event
		}
		client.SendDomainEvent(watch.Event{Type: watch.Modified, Object: domain})
	}
}

func (n *NotifierImpl) StartDomainNotifier(domainConn *VirtBlocksImpl, deleteNotificationSent chan watch.Event, vmiUID types.UID, qemuAgentPollerInterval *time.Duration) error {
	eventChan := make(chan libvirtEvent, 10)

	// Run the event process logic in a separate go-routine to not block libvirt
	go func() {
		var interfaceStatuses *[]api.InterfaceStatus
		for {
			select {
			case event := <-eventChan:
				domain := util.NewDomainFromName(event.Domain, vmiUID)
				eventCallback(domainConn, domain, n, deleteNotificationSent, interfaceStatuses)
				log.Log.Info("processed event")
			}
		}
	}()

	go func() {
		// TODO, do something event driven here, like listen to qmp
		state := api.NoState
		reason := api.ReasonNonExistent
		newState := api.NoState
		newReason := api.ReasonNonExistent
		for {
			time.Sleep(1 * time.Second)
			changed := false
			d, err := domainConn.GetDomain()
			if isNotFound(err) {
				if state != api.NoState || reason != api.ReasonNonExistent {
					changed = true
				}
			} else if err != nil {
				log.Log.Reason(err).Info("Could not determine domain state.")
				return
			}
			if newState, newReason, err = d.GetState(); err != nil {
				log.Log.Reason(err).Info("Could not determine domain state.")
				return
			} else if newState != state || newReason != reason {
				changed = true
			}

			if changed {
				select {
				case eventChan <- libvirtEvent{}:
				default:
					log.Log.Infof("Libvirt event channel is full, dropping event.")
				}
				state = newState
				reason = newReason
			}
		}
	}()
	return nil
}

func (n *NotifierImpl) SendK8sEvent(vmi *v1.VirtualMachineInstance, severity string, reason string, message string) error {

	vmiRef, err := reference.GetReference(v1.Scheme, vmi)
	if err != nil {
		return err
	}

	event := k8sv1.Event{
		InvolvedObject: *vmiRef,
		Type:           severity,
		Reason:         reason,
		Message:        message,
	}

	json, err := json.Marshal(event)
	if err != nil {
		return err
	}

	request := notifyv1.K8SEventRequest{
		EventJSON: json,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	response, err := n.v1client.HandleK8SEvent(ctx, &request)

	if err != nil {
		return err
	} else if response.Success != true {
		msg := fmt.Sprintf("failed to notify k8s event: %s", response.Message)
		return fmt.Errorf(msg)
	}

	return nil
}

func (n *NotifierImpl) Close() {
	n.conn.Close()
}

type Notifier interface {
	SendK8sEvent(vmi *v1.VirtualMachineInstance, severity string, reason string, message string) error
	Close()
}
