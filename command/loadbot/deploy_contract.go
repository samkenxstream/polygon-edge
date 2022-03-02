package loadbot

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/0xPolygon/polygon-edge/command/loadbot/generator"
	"github.com/0xPolygon/polygon-edge/helper/tests"
	txpoolOp "github.com/0xPolygon/polygon-edge/txpool/proto"
	"github.com/0xPolygon/polygon-edge/types"

	"github.com/umbracle/go-web3"
	"github.com/umbracle/go-web3/jsonrpc"
)

func (l *Loadbot) deployContract(
	grpcClient txpoolOp.TxnPoolOperatorClient,
	jsonClient *jsonrpc.Client,
	receiptTimeout time.Duration) error {
	// if this is a regular transfer skip token deployment
	if l.cfg.GeneratorMode == transfer {
		return nil
	}

	start := time.Now()

	// deploy SC
	txHash, err := l.executeTxn(grpcClient)
	if err != nil {
		l.generator.MarkFailedContractTxn(&generator.FailedContractTxnInfo{
			TxHash: txHash.String(),
			Error: &generator.TxnError{
				Error:     err,
				ErrorType: generator.AddErrorType,
			},
		})
		atomic.AddUint64(&l.metrics.FailedContractTransactionsCount, 1)

		return fmt.Errorf("could not execute transaction, %w", err)
	}

	// set timeout
	ctx, cancel := context.WithTimeout(context.Background(), receiptTimeout)
	defer cancel()

	// and wait for receipt
	receipt, err := tests.WaitForReceipt(ctx, jsonClient.Eth(), txHash)
	// initialize gas metrics map with block nuber as index
	l.metrics.ContractGasMetrics.Blocks[receipt.BlockNumber] = GasMetrics{}
	if err != nil {
		l.generator.MarkFailedContractTxn(&generator.FailedContractTxnInfo{
			TxHash: txHash.String(),
			Error: &generator.TxnError{
				Error:     err,
				ErrorType: generator.ReceiptErrorType,
			},
		})
		atomic.AddUint64(&l.metrics.FailedContractTransactionsCount, 1)

		return fmt.Errorf("could not get the receipt, %w", err)
	}

	end := time.Now()
	// fetch contract address
	l.metrics.ContractAddress = receipt.ContractAddress
	// set contract address in order to get new example txn and gas estimate
	l.generator.SetContractAddress(types.StringToAddress(
		receipt.ContractAddress.String(),
	))

	// we're done with SC deployment
	// we defined SC address and
	// now get new gas estimates for CS token transfers
	if l.cfg.GasLimit == nil {
		// Get the gas estimate
		exampleTxn, err := l.generator.GetExampleTransaction()
		if err != nil {
			return fmt.Errorf("unable to get example transaction, %w", err)
		}

		// No gas limit specified, query the network for an estimation
		gasEstimate, estimateErr := estimateGas(jsonClient, exampleTxn)
		if estimateErr != nil {
			return fmt.Errorf("unable to get gas estimate, %w", err)
		}

		l.generator.SetGasEstimate(gasEstimate)
	}

	// record contract deployment metrics
	l.metrics.ContractDeploymentDuration.reportTurnAroundTime(
		txHash,
		&metadata{
			turnAroundTime: end.Sub(start),
			blockNumber:    receipt.BlockNumber,
		},
	)
	// calculate contract deployment metrics
	// by getting gas info from each block number we recorded from the receipt
	for k, v := range l.metrics.ContractGasMetrics.Blocks {
		blockInfom, err := jsonClient.Eth().GetBlockByNumber(web3.BlockNumber(k), false)
		if err != nil {
			return fmt.Errorf("could not fetch block by number, %w", err)
		}

		v.GasLimit = blockInfom.GasLimit
		v.GasUsed = blockInfom.GasUsed
		l.metrics.ContractGasMetrics.Blocks[k] = v
	}

	l.metrics.ContractDeploymentDuration.calcTurnAroundMetrics()
	l.metrics.ContractDeploymentDuration.TotalExecTime = end.Sub(start)

	return nil
}