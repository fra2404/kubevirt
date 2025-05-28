package vnc

import (
	"io"
	"net"
	"sync"

	"kubevirt.io/client-go/log"
)

// VNCProxy gestisce il forwarding tra socket TCP e Unix
type VNCProxy struct {
	tcpPort    int
	unixSocket string
	listener   net.Listener
	stopCh     chan struct{}
	wg         sync.WaitGroup
}

// NewVNCProxy crea un nuovo proxy VNC
func NewVNCProxy(unixSocketPath string, tcpPort int) *VNCProxy {
	return &VNCProxy{
		unixSocket: unixSocketPath,
		tcpPort:    tcpPort,
		stopCh:     make(chan struct{}),
	}
}

// Soluzione ottimale
func (p *VNCProxy) Start() error {
	// Modifica questa sezione: ascolta SOLO su 0.0.0.0 (che include implicitamente localhost)
	 addr := net.TCPAddr{IP: net.ParseIP("0.0.0.0"), Port: p.tcpPort}
    listener, err := net.ListenTCP("tcp", &addr)
    if err != nil {
        return err
    }

    p.listener = listener
    log.Log.Infof("VNC proxy in ascolto su 0.0.0.0:%d", p.tcpPort)

	// Gestisci le connessioni (versione semplificata)
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			select {
			case <-p.stopCh:
				return
			default:
				conn, err := listener.Accept()
				if err != nil {
					select {
					case <-p.stopCh:
						return
					default:
						log.Log.Reason(err).Error("Errore nell'accettare connessione VNC")
						continue
					}
				}

				p.wg.Add(1)
				go p.handleConnection(conn)
			}
		}
	}()

	return nil
}

// handleConnection gestisce una singola connessione client
func (p *VNCProxy) handleConnection(clientConn net.Conn) {
	defer p.wg.Done()
	defer clientConn.Close()

	log.Log.Infof("Nuova connessione VNC da %s", clientConn.RemoteAddr().String())

	// Connessione al socket unix VNC
	unixConn, err := net.Dial("unix", p.unixSocket)
	if err != nil {
		log.Log.Reason(err).Errorf("Impossibile connettersi alla socket unix %s", p.unixSocket)
		return
	}
	defer unixConn.Close()

	// Gestione del forwarding bidirezionale
	waitCh := make(chan struct{})

	// Inoltra dati client -> unix socket
	go func() {
		_, err := io.Copy(unixConn, clientConn)
		if err != nil {
			log.Log.V(3).Reason(err).Infof("Trasferimento client -> server VNC terminato")
		}
		close(waitCh)
	}()

	// Inoltra dati unix socket -> client
	_, err = io.Copy(clientConn, unixConn)
	if err != nil {
		log.Log.V(3).Reason(err).Infof("Trasferimento server -> client VNC terminato")
	}

	<-waitCh
	log.Log.Infof("Connessione VNC con %s terminata", clientConn.RemoteAddr().String())
}

// Stop ferma il proxy VNC
func (p *VNCProxy) Stop() {
	close(p.stopCh)
	if p.listener != nil {
		p.listener.Close()
	}
	p.wg.Wait()
	log.Log.Info("VNC proxy fermato")
}
