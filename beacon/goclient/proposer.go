package goclient

import (
	"fmt"

	"github.com/attestantio/go-eth2-client/spec"
	"github.com/attestantio/go-eth2-client/spec/bellatrix"
	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/pkg/errors"
)

// GetBeaconBlock returns beacon block by the given slot and committee index
func (gc *goClient) GetBeaconBlock(slot phase0.Slot, graffiti, randao []byte) (*bellatrix.BeaconBlock, error) {
	// TODO need to support blinded?
	// TODO what with fee recipient?
	sig := phase0.BLSSignature{}
	copy(sig[:], randao[:])

	beaconBlockRoot, err := gc.client.BeaconBlockProposal(gc.ctx, slot, sig, graffiti)
	if err != nil {
		return nil, err
	}

	switch beaconBlockRoot.Version {
	case spec.DataVersionBellatrix:
		return beaconBlockRoot.Bellatrix, nil
	default:
		return nil, errors.New(fmt.Sprintf("beacon block version %s not supported", beaconBlockRoot.Version))
	}
}

// SubmitBeaconBlock submit the block to the node
func (gc *goClient) SubmitBeaconBlock(block *bellatrix.SignedBeaconBlock) error {
	versionedBlock := &spec.VersionedSignedBeaconBlock{
		Version:   spec.DataVersionBellatrix,
		Bellatrix: block,
	}

	return gc.client.SubmitBeaconBlock(gc.ctx, versionedBlock)
}