package msg

import (
	"context"
	"errors"

	"github.com/OpenIMSDK/Open-IM-Server/pkg/common/constant"
	"github.com/OpenIMSDK/Open-IM-Server/pkg/common/log"
	"github.com/OpenIMSDK/Open-IM-Server/pkg/errs"
	"github.com/OpenIMSDK/Open-IM-Server/pkg/proto/msg"
	"github.com/OpenIMSDK/Open-IM-Server/pkg/proto/sdkws"
	"github.com/redis/go-redis/v9"
)

func (m *msgServer) GetConversationsHasReadAndMaxSeq(ctx context.Context, req *msg.GetConversationsHasReadAndMaxSeqReq) (*msg.GetConversationsHasReadAndMaxSeqResp, error) {
	conversationIDs, err := m.ConversationLocalCache.GetConversationIDs(ctx, req.UserID)
	if err != nil {
		return nil, err
	}
	hasReadSeqs, err := m.MsgDatabase.GetHasReadSeqs(ctx, req.UserID, conversationIDs)
	if err != nil {
		return nil, err
	}
	conversations, err := m.Conversation.GetConversations(ctx, req.UserID, conversationIDs)
	if err != nil {
		return nil, err
	}
	var conversationMaxSeqMap = make(map[string]int64)
	for _, conversation := range conversations {
		if conversation.MaxSeq != 0 {
			conversationMaxSeqMap[conversation.ConversationID] = conversation.MaxSeq
		}
	}
	maxSeqs, err := m.MsgDatabase.GetMaxSeqs(ctx, conversationIDs)
	if err != nil {
		return nil, err
	}
	resp := &msg.GetConversationsHasReadAndMaxSeqResp{Seqs: make(map[string]*msg.Seqs)}
	for conversarionID, maxSeq := range maxSeqs {
		resp.Seqs[conversarionID] = &msg.Seqs{
			HasReadSeq: hasReadSeqs[conversarionID],
			MaxSeq:     maxSeq,
		}
		if v, ok := conversationMaxSeqMap[conversarionID]; ok {
			resp.Seqs[conversarionID].MaxSeq = v
		}
	}
	return resp, nil
}

func (m *msgServer) SetConversationHasReadSeq(ctx context.Context, req *msg.SetConversationHasReadSeqReq) (resp *msg.SetConversationHasReadSeqResp, err error) {
	maxSeq, err := m.MsgDatabase.GetMaxSeq(ctx, req.ConversationID)
	if err != nil {
		return
	}
	if req.HasReadSeq > maxSeq {
		return nil, errs.ErrArgs.Wrap("hasReadSeq must not be bigger than maxSeq")
	}
	if err := m.MsgDatabase.SetHasReadSeq(ctx, req.UserID, req.ConversationID, req.HasReadSeq); err != nil {
		return nil, err
	}
	if err = m.sendMarkAsReadNotification(ctx, req.ConversationID, constant.SingleChatType, req.UserID, req.UserID, nil, req.HasReadSeq); err != nil {
		return
	}
	return &msg.SetConversationHasReadSeqResp{}, nil
}

func (m *msgServer) MarkMsgsAsRead(ctx context.Context, req *msg.MarkMsgsAsReadReq) (resp *msg.MarkMsgsAsReadResp, err error) {
	if len(req.Seqs) < 1 {
		return nil, errs.ErrArgs.Wrap("seqs must not be empty")
	}
	maxSeq, err := m.MsgDatabase.GetMaxSeq(ctx, req.ConversationID)
	if err != nil {
		return
	}
	hasReadSeq := req.Seqs[len(req.Seqs)-1]
	if hasReadSeq > maxSeq {
		return nil, errs.ErrArgs.Wrap("hasReadSeq must not be bigger than maxSeq")
	}
	conversation, err := m.Conversation.GetConversation(ctx, req.UserID, req.ConversationID)
	if err != nil {
		return
	}
	if err = m.MsgDatabase.MarkSingleChatMsgsAsRead(ctx, req.UserID, req.ConversationID, req.Seqs); err != nil {
		return
	}
	currentHasReadSeq, err := m.MsgDatabase.GetHasReadSeq(ctx, req.UserID, req.ConversationID)
	if err != nil && errors.Unwrap(err) != redis.Nil {
		return
	}
	if hasReadSeq > currentHasReadSeq {
		err = m.MsgDatabase.SetHasReadSeq(ctx, req.UserID, req.ConversationID, hasReadSeq)
		if err != nil {
			return
		}
	}
	if err = m.sendMarkAsReadNotification(ctx, req.ConversationID, conversation.ConversationType, req.UserID, m.conversationAndGetRecvID(conversation, req.UserID), req.Seqs, hasReadSeq); err != nil {
		return
	}
	return &msg.MarkMsgsAsReadResp{}, nil
}

func (m *msgServer) MarkConversationAsRead(ctx context.Context, req *msg.MarkConversationAsReadReq) (resp *msg.MarkConversationAsReadResp, err error) {
	conversation, err := m.Conversation.GetConversation(ctx, req.UserID, req.ConversationID)
	if err != nil {
		return
	}
	hasReadSeq, err := m.MsgDatabase.GetHasReadSeq(ctx, req.UserID, req.ConversationID)
	if err != nil && errs.Unwrap(err) != redis.Nil {
		return
	}
	log.ZDebug(ctx, "MarkConversationAsRead", "hasReadSeq", hasReadSeq, "req.HasReadSeq", req.HasReadSeq)
	if len(req.Seqs) > 0 {
		log.ZDebug(ctx, "MarkConversationAsRead", "seqs", req.Seqs, "conversationID", req.ConversationID)
		if err = m.MsgDatabase.MarkSingleChatMsgsAsRead(ctx, req.UserID, req.ConversationID, req.Seqs); err != nil {
			return
		}
	}
	if req.HasReadSeq > hasReadSeq {
		err = m.MsgDatabase.SetHasReadSeq(ctx, req.UserID, req.ConversationID, req.HasReadSeq)
		if err != nil {
			return
		}
		hasReadSeq = req.HasReadSeq
	}
	if err = m.sendMarkAsReadNotification(ctx, req.ConversationID, conversation.ConversationType, req.UserID, m.conversationAndGetRecvID(conversation, req.UserID), req.Seqs, hasReadSeq); err != nil {
		return
	}
	return &msg.MarkConversationAsReadResp{}, nil
}

func (m *msgServer) sendMarkAsReadNotification(ctx context.Context, conversationID string, sesstionType int32, sendID, recvID string, seqs []int64, hasReadSeq int64) error {
	tips := &sdkws.MarkAsReadTips{
		MarkAsReadUserID: sendID,
		ConversationID:   conversationID,
		Seqs:             seqs,
		HasReadSeq:       hasReadSeq,
	}
	m.notificationSender.NotificationWithSesstionType(ctx, sendID, recvID, constant.HasReadReceipt, sesstionType, tips)
	return nil
}
