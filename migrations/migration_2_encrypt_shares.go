package migrations

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"fmt"

	"github.com/bloxapp/ssv/storage/basedb"

	"go.uber.org/zap"
)

var migration_2_encrypt_shares = Migration{
	Name: "migration_2_encrypt_shares",
	Run: func(ctx context.Context, logger *zap.Logger, opt Options, key []byte, completed CompletedFunc) error {
		return opt.Db.Update(func(txn basedb.Txn) error {
			nodeStorage, err := opt.nodeStorage(logger)
			if err != nil {
				return fmt.Errorf("failed to get node storage: %w", err)
			}
			operatorKey, found, err := nodeStorage.GetPrivateKey()
			if err != nil {
				return fmt.Errorf("failed to get private key: %w", err)
			}
			if !found {
				return completed(txn)
			}
			signerStorage := opt.signerStorage(logger)
			accounts, err := signerStorage.ListAccountsTxn(txn)
			if err != nil {
				return fmt.Errorf("failed to list accounts: %w", err)
			}
			keyBytes := x509.MarshalPKCS1PrivateKey(operatorKey)
			hash := sha256.Sum256(keyBytes)
			keyString := fmt.Sprintf("%x", hash)
			err = signerStorage.SetEncryptionKey(keyString)
			if err != nil {
				return fmt.Errorf("failed to set encryption key: %w", err)
			}
			for _, account := range accounts {
				err := signerStorage.SaveAccountTxn(txn, account)
				if err != nil {
					return fmt.Errorf("failed to save account %s: %w", account, err)
				}
			}
			return completed(txn)
		})
	},
}