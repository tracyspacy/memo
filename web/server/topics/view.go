package topics

import (
	"github.com/jchavannes/jgo/jerr"
	"github.com/jchavannes/jgo/web"
	"github.com/memocash/memo/app/auth"
	"github.com/memocash/memo/app/cache"
	"github.com/memocash/memo/app/db"
	"github.com/memocash/memo/app/profile"
	"github.com/memocash/memo/app/res"
	"net/http"
	"net/url"
)

var viewRoute = web.Route{
	Pattern: res.UrlTopicView + "/" + urlTopicName.UrlPart(),
	Handler: func(r *web.Response) {
		preHandler(r)
		topicRaw := r.Request.GetUrlNamedQueryVariable(urlTopicName.Id)
		unescaped, err := url.QueryUnescape(topicRaw)
		if err != nil {
			r.Error(jerr.Get("error unescaping topic", err), http.StatusUnprocessableEntity)
			return
		}
		var userPkHash []byte
		var userId uint
		if auth.IsLoggedIn(r.Session.CookieId) {
			user, err := auth.GetSessionUser(r.Session.CookieId)
			if err != nil {
				r.Error(jerr.Get("error getting session user", err), http.StatusInternalServerError)
				return
			}
			key, err := db.GetKeyForUser(user.Id)
			if err != nil {
				r.Error(jerr.Get("error getting key for user", err), http.StatusInternalServerError)
				return
			}
			userPkHash = key.PkHash
			userId = user.Id
		}
		topicPosts, err := profile.GetPostsForTopic(unescaped, userPkHash, 0)
		if err != nil {
			r.Error(jerr.Get("error getting topic posts from db", err), http.StatusInternalServerError)
			return
		}
		if len(topicPosts) == 0 {
			jerr.New("no posts for topic").Print()
			r.SetRedirect(res.UrlTopics)
			return
		}
		if len(userPkHash) > 0 {
			err = profile.AttachReputationToPosts(topicPosts)
			if err != nil {
				r.Error(jerr.Get("error attaching reputation to posts", err), http.StatusInternalServerError)
				return
			}
		}
		err = profile.AttachLikesToPosts(topicPosts)
		if err != nil {
			r.Error(jerr.Get("error attaching likes to posts", err), http.StatusInternalServerError)
			return
		}
		err = profile.AttachReplyCountToPosts(topicPosts)
		if err != nil {
			r.Error(jerr.Get("error attaching reply counts to posts", err), http.StatusInternalServerError)
			return
		}
		var lastLikeId uint
		for _, topicPost := range topicPosts {
			for _, like := range topicPost.Likes {
				if like.Id > lastLikeId {
					lastLikeId = like.Id
				}
			}
		}
		if len(userPkHash) > 0 {
			lastPostId, err := db.GetLastTopicPostId(userPkHash, unescaped)
			if err != nil {
				r.Error(jerr.Get("error getting last topic post id from db", err), http.StatusInternalServerError)
				return
			}
			var newLastPostId= lastPostId
			for _, topicPost := range topicPosts {
				if topicPost.Memo.Id > newLastPostId {
					newLastPostId = topicPost.Memo.Id
				}
			}
			if newLastPostId > lastPostId {
				err = db.SetLastTopicPostId(userPkHash, unescaped, newLastPostId)
				if err != nil {
					r.Error(jerr.Get("error setting last topic post id in db", err), http.StatusInternalServerError)
					return
				}
			}
		}
		err = profile.SetShowMediaForPosts(topicPosts, userId)
		if err != nil {
			r.Error(jerr.Get("error setting show media for posts", err), http.StatusInternalServerError)
			return
		}
		if len(userPkHash) > 0 {
			isFollowing, err := db.IsFollowingTopic(userPkHash, unescaped)
			if err != nil {
				r.Error(jerr.Get("error checking if user is following topic", err), http.StatusInternalServerError)
				return
			}
			r.Helper["IsFollowingTopic"] = isFollowing
		}
		followerCount, err := db.GetFollowerCountForTopic(unescaped)
		if err != nil {
			r.Error(jerr.Get("error getting follower count for topic", err), http.StatusInternalServerError)
			return
		}
		lastTopicList, err := cache.GetLastTopicList(r.Session.CookieId)
		if err != nil {
			jerr.Get("error getting last topic list", err).Print()
		}
		r.Helper["Title"] = "Memo - Topic - " + topicPosts[0].Memo.Topic
		r.Helper["Topic"] = topicPosts[0].Memo.Topic
		r.Helper["TopicEncoded"] = topicPosts[0].Memo.GetUrlEncodedTopic()
		r.Helper["Posts"] = topicPosts
		r.Helper["FollowerCount"] = followerCount
		r.Helper["FirstPostId"] = topicPosts[0].Memo.Id
		r.Helper["LastPostId"] = topicPosts[len(topicPosts)-1].Memo.Id
		r.Helper["LastLikeId"] = lastLikeId
		r.Helper["LastTopicList"] = lastTopicList
		r.RenderTemplate(res.TmplTopicView)
	},
}
