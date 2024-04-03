package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/b2network/b2committer/internal/schema"
	"github.com/b2network/b2committer/internal/svc"
	"github.com/b2network/b2committer/pkg/event/zkevm"
	"github.com/b2network/b2committer/pkg/log"
	"github.com/ethereum/go-ethereum/common"
	"github.com/pkg/errors"
	"os"
	"strconv"
	"time"
)

type CollectionSequenceBatches struct {
	IsCompleted     bool
	SequenceBatches []*SequenceBatchesAndTxHash
	StartBatchNum   uint64
	EndBatchNum     uint64
}

type SequenceBatchesAndTxHash struct {
	NumBatch uint64
	TxHash   string
}

func SequenceBatches(ctx *svc.ServiceContext) {
	for {
		var dbProposal schema.Proposal
		err := ctx.DB.Where("status = ? and generate_details_file = ? and winner = ?", schema.ProposalCommitting, false,
			ctx.B2NodeConfig.Address).Order("proposal_id asc").First(&dbProposal).Error
		collectionSequenceBatches, err := GetSequenceBatchesFromStartBatchNum(ctx, dbProposal.StartBatchNum, dbProposal.EndBatchNum)
		if err != nil {
			log.Errorf("[Handler.SequenceBatches][GetSequenceBatchesFromStartBatchNum] error info: %s", errors.WithStack(err).Error())
			time.Sleep(10 * time.Second)
			continue
		}
		if !collectionSequenceBatches.IsCompleted {
			log.Errorf("[Handler.SequenceBatches] sync batches not completed")
			time.Sleep(10 * time.Second)
			continue
		}
		sequenceBatchesMap, err := GetSequenceBatchesDetails(ctx, collectionSequenceBatches.SequenceBatches)
		if err != nil {
			log.Errorf("[Handler.SequenceBatches][GetSequenceBatchesDetails] error info: %s", errors.WithStack(err).Error())
			time.Sleep(3 * time.Second)
			continue
		}
		res, err := WriteFile(ctx, dbProposal.StartBatchNum, dbProposal.EndBatchNum, sequenceBatchesMap)
		if err != nil {
			log.Errorf("[Handler.SequenceBatches][WriteFile] error info: %s", errors.WithStack(err).Error())
			time.Sleep(3 * time.Second)
			continue
		}
		if res {
			err = ctx.DB.Model(&dbProposal).Update("generate_details_file", true).Error
			if err != nil {
				log.Errorf("[Handler.SequenceBatches][Update] error info: %s", errors.WithStack(err).Error())
				time.Sleep(3 * time.Second)
				continue
			}
		}
	}
}

func GetSequenceBatchesFromStartBatchNum(ctx *svc.ServiceContext, startBatchNum uint64, endBatchNum uint64) (*CollectionSequenceBatches, error) {
	isCompleted := false
	events := make([]schema.SyncEvent, 0)
	err := ctx.DB.Table("sync_events").Select("*, JSON_EXTRACT(data, '$.numBatch') as numBatch ").
		Where(" event_name = ? and JSON_EXTRACT(data, '$.numBatch') between ? and ?", "sequenceBatches",
			startBatchNum, endBatchNum).Order("numBatch").Find(&events).Error
	if err != nil {
		return nil, fmt.Errorf("[GetSequenceBatchesFromStartBatchNum] dbbase err: %s", err)
	}

	sequenceBatchesAndTxHashes := make([]*SequenceBatchesAndTxHash, 0)
	for _, event := range events {
		sequenceBatch := &zkevm.SequenceBatches{}
		err = sequenceBatch.ToObj(event.Data)
		if err != nil {
			return nil, fmt.Errorf("[GetSequenceBatchesFromStartBatchNum] parse event data error: %s", errors.WithStack(err))
		}
		SequenceBatchesAndTxHash := &SequenceBatchesAndTxHash{
			NumBatch: sequenceBatch.BatchNum,
			TxHash:   event.TxHash,
		}
		sequenceBatchesAndTxHashes = append(sequenceBatchesAndTxHashes, SequenceBatchesAndTxHash)
		if sequenceBatch.BatchNum == endBatchNum {
			isCompleted = true
		}
	}

	return &CollectionSequenceBatches{
		StartBatchNum:   startBatchNum,
		EndBatchNum:     endBatchNum,
		SequenceBatches: sequenceBatchesAndTxHashes,
		IsCompleted:     isCompleted,
	}, nil
}

func GetSequenceBatchesDetails(ctx *svc.ServiceContext, sequenceBatches []*SequenceBatchesAndTxHash) (map[uint64][]byte, error) {
	sequenceBatchesMap := make(map[uint64][]byte)
	for _, sequenceBatch := range sequenceBatches {
		txHash := sequenceBatch.TxHash
		tx, _, err := ctx.RPC.TransactionByHash(context.Background(), common.HexToHash(txHash))
		if err != nil {
			return nil, fmt.Errorf("[GetSequenceBatchesDetails] get tx error: %s", errors.WithStack(err))
		}
		sequenceBatchesMap[sequenceBatch.NumBatch] = tx.Data()
	}
	return sequenceBatchesMap, nil
}

func WriteFile(ctx *svc.ServiceContext, startBatchNum uint64, endBatchNum uint64, sequenceBatchesMap map[uint64][]byte) (bool, error) {
	fileName := strconv.FormatUint(startBatchNum, 10) + "-" + strconv.FormatUint(endBatchNum, 10) + ".json"
	jsonData, err := json.Marshal(sequenceBatchesMap)
	if err != nil {
		log.Errorf("[WriteFile] json marshal error: %s", errors.WithStack(err))
		return false, err
	}
	path, err := os.Getwd()
	if err != nil {
		log.Errorf("[WriteFile] get current path error: %s", errors.WithStack(err))
		return false, err
	}
	filePath := path + "/" + ctx.Config.BatchPath
	_, err2 := os.Stat(filePath)
	if os.IsNotExist(err2) {
		errDir := os.MkdirAll(filePath, os.ModePerm)
		if errDir != nil {
			log.Errorf("[WriteFile] create dir error: %s", errors.WithStack(errDir))
			return false, errDir
		}
	}
	file, err := os.Create(filePath + "/" + fileName)
	if err != nil {
		log.Errorf("[WriteFile] create file error: %s", errors.WithStack(err))
		return false, err
	}
	defer func(file *os.File) {
		err := file.Close()
		if err != nil {
			log.Errorf("[WriteFile] close file error: %s", errors.WithStack(err))
		}
	}(file)

	_, err = file.Write(jsonData)
	if err != nil {
		log.Errorf("[WriteFile] write file error: %s", errors.WithStack(err))
		return false, nil
	}
	return true, nil
}