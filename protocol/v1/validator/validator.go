package validator

import (
	"context"
	"github.com/bloxapp/ssv/protocol/v1/qbft"
	"github.com/pkg/errors"
	"go.uber.org/zap"
	"time"

	forksprotocol "github.com/bloxapp/ssv/protocol/forks"
	beaconprotocol "github.com/bloxapp/ssv/protocol/v1/blockchain/beacon"
	"github.com/bloxapp/ssv/protocol/v1/message"
	p2pprotocol "github.com/bloxapp/ssv/protocol/v1/p2p"
	"github.com/bloxapp/ssv/protocol/v1/qbft/controller"
	qbftstorage "github.com/bloxapp/ssv/protocol/v1/qbft/storage"
	"github.com/bloxapp/ssv/protocol/v1/queue/worker"
	"github.com/bloxapp/ssv/utils/format"
)

type IValidator interface {
	Start()
	ExecuteDuty(slot uint64, duty *beaconprotocol.Duty)
	ProcessMsg(msg *message.SSVMessage) //TODO need to be as separate interface?
	GetShare() *message.Share
}

type Options struct {
	Context                    context.Context
	Logger                     *zap.Logger
	IbftStorage                qbftstorage.QBFTStore
	Network                    beaconprotocol.Network
	P2pNetwork                 p2pprotocol.Network
	Beacon                     beaconprotocol.Beacon
	Share                      *message.Share
	ForkVersion                forksprotocol.ForkVersion
	Signer                     beaconprotocol.Signer
	SyncRateLimit              time.Duration
	SignatureCollectionTimeout time.Duration
	ReadMode                   bool
}

type Validator struct {
	ctx        context.Context
	logger     *zap.Logger
	network    beaconprotocol.Network
	p2pNetwork p2pprotocol.Network
	beacon     beaconprotocol.Beacon
	share      *message.Share
	signer     beaconprotocol.Signer
	worker     *worker.Worker

	// signature
	signatureState SignatureState

	ibfts controller.Controllers

	// flags
	readMode bool
}

func NewValidator(opt *Options) IValidator {
	logger := opt.Logger.With(zap.String("pubKey", opt.Share.PublicKey.SerializeToHexStr())).
		With(zap.Uint64("node_id", uint64(opt.Share.NodeID)))

	workerCfg := &worker.Config{
		Ctx:          opt.Context,
		WorkersCount: 1,   // TODO flag
		Buffer:       100, // TODO flag
	}
	queueWorker := worker.NewWorker(workerCfg)

	ibfts := setupIbfts(opt, logger)

	logger.Debug("new validator instance was created", zap.Strings("operators ids", opt.Share.HashOperators()))
	return &Validator{
		ctx:            opt.Context,
		logger:         logger,
		network:        opt.Network,
		p2pNetwork:     opt.P2pNetwork,
		beacon:         opt.Beacon,
		share:          opt.Share,
		signer:         opt.Signer,
		ibfts:          ibfts,
		worker:         queueWorker,
		signatureState: SignatureState{signatureCollectionTimeout: opt.SignatureCollectionTimeout},
		readMode:       opt.ReadMode,
	}
}

func (v *Validator) Start() {
	// start queue workers
	v.worker.AddHandler(v.messageHandler)
}

func (v *Validator) GetShare() *message.Share {
	// TODO need lock?
	return v.share
}

// ProcessMsg processes a new msg, returns true if Decided, non nil byte slice if Decided (Decided value) and error
// Decided returns just once per instance as true, following messages (for example additional commit msgs) will not return Decided true
func (v *Validator) ProcessMsg(msg *message.SSVMessage) /*(bool, []byte, error)*/ {
	// check duty type and handle accordingly
	if v.readMode {
		// synchronize process
		err := v.messageHandler(msg) // TODO return error?
		if err != nil {
			return
		}
		return
	}
	// put msg to queue in order to preform async process and prevent blocking validatorController
	switch msg.GetIdentifier() {
	case // attester
		v.AttestQueue.TryEnqueue(msg)
	case // propose
		v.proposeQueue.TryEnqueue(msg)
	}
	v.worker.TryEnqueue(msg)
}

// messageHandler process message from queue,
func (v *Validator) messageHandler(msg *message.SSVMessage) error {
	// validation
	if err := v.validateMessage(msg); err != nil {
		// TODO need to return error?
		v.logger.Error("message validation failed", zap.Error(err))
		return nil
	}

	ibftController := v.ibfts.ControllerForIdentifier(msg.GetIdentifier())

	switch msg.GetType() {
	case message.SSVConsensusMsgType:
		signedMsg := &message.SignedMessage{}
		if err := signedMsg.Decode(msg.GetData()); err != nil {
			return errors.Wrap(err, "could not get post consensus Message from SSVMessage")
		}
		return v.processConsensusMsg(ibftController, signedMsg)

	case message.SSVPostConsensusMsgType:
		signedMsg := &message.SignedPostConsensusMessage{}
		if err := signedMsg.Decode(msg.GetData()); err != nil {
			return errors.Wrap(err, "could not get post consensus Message from network Message")
		}
		return v.processPostConsensusSig(ibftController, signedMsg)
	case message.SSVSyncMsgType:
		panic("need to implement!")
	}
	return nil
}

// setupRunners return duty runners map with all the supported duty types
func setupIbfts(opt *Options, logger *zap.Logger) map[beaconprotocol.RoleType]controller.IController {
	ibfts := make(map[beaconprotocol.RoleType]controller.IController)
	ibfts[beaconprotocol.RoleTypeAttester] = setupIbftController(beaconprotocol.RoleTypeAttester, logger, opt.IbftStorage, opt.P2pNetwork, opt.Share, opt.ForkVersion, opt.Signer, opt.SyncRateLimit)
	return ibfts
}

func setupIbftController(
	role beaconprotocol.RoleType,
	logger *zap.Logger,
	ibftStorage qbftstorage.QBFTStore,
	network p2pprotocol.Network,
	share *message.Share,
	forkVersion forksprotocol.ForkVersion,
	signer beaconprotocol.Signer,
	syncRateLimit time.Duration) controller.IController {
	identifier := []byte(format.IdentifierFormat(share.PublicKey.Serialize(), role.String()))

	return controller.New(
		role,
		identifier,
		logger,
		ibftStorage,
		network,
		qbft.DefaultConsensusParams(),
		share,
		forkVersion,
		signer,
		syncRateLimit)
}
