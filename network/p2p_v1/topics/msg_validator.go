package topics

import (
	"bytes"
	"context"
	"github.com/bloxapp/ssv/network/forks"
	"github.com/bloxapp/ssv/protocol"
	"github.com/libp2p/go-libp2p-core/peer"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"go.uber.org/zap"
)

// MsgValidatorFunc represents a message validator
type MsgValidatorFunc = func(ctx context.Context, p peer.ID, msg *pubsub.Message) pubsub.ValidationResult

// NewSSVMsgValidator creates a new msg validator that validates message structure,
// and checks that the message was sent on the right topic.
// TODO: remove logs, break into smaller validators?
func NewSSVMsgValidator(plogger *zap.Logger, fork forks.Fork, self peer.ID) func(ctx context.Context, p peer.ID, msg *pubsub.Message) pubsub.ValidationResult {
	return func(ctx context.Context, p peer.ID, msg *pubsub.Message) pubsub.ValidationResult {
		logger := plogger.With(zap.String("topic", msg.GetTopic()), zap.String("peer", p.String()))
		//logger.Debug("xxx validating msg")
		if len(msg.Data) == 0 {
			logger.Debug("invalid: no data")
			reportValidationResult(validationResultNoData)
			return pubsub.ValidationReject
		}
		if bytes.Equal([]byte(p), []byte(self)) {
			logger.Debug("valid:  our node's message")
			reportValidationResult(validationResultSelf)
			return pubsub.ValidationAccept
		}
		smsg := protocol.SSVMessage{}
		if err := smsg.Decode(msg.Data); err != nil {
			// can't decode message
			logger.Debug("invalid: can't decode message", zap.Error(err))
			reportValidationResult(validationResultEncoding)
			return pubsub.ValidationReject
		}
		topic := fork.ValidatorTopicID(smsg.ID.GetValidatorPK())
		if topic != msg.GetTopic() {
			// wrong topic
			logger.Debug("invalid: wrong topic", zap.String("actual", topic),
				zap.String("expected", msg.GetTopic()),
				zap.ByteString("smsg.ID", smsg.ID))
			reportValidationResult(validationResultTopic)
			return pubsub.ValidationReject
		}
		logger.Debug("valid", zap.ByteString("smsg.ID", smsg.ID))
		reportValidationResult(validationResultValid)
		return pubsub.ValidationAccept
	}
}

//// CombineMsgValidators executes multiple validators
//func CombineMsgValidators(validators ...MsgValidatorFunc) MsgValidatorFunc {
//	return func(ctx context.Context, p peer.ID, msg *pubsub.Message) pubsub.ValidationResult {
//		res := pubsub.ValidationAccept
//		for _, v := range validators {
//			if res = v(ctx, p, msg); res == pubsub.ValidationReject {
//				break
//			}
//		}
//		return res
//	}
//}
