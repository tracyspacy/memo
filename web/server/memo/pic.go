package memo

import (
	"fmt"
	"github.com/btcsuite/btcutil"
	"github.com/jchavannes/jgo/jerr"
	"github.com/jchavannes/jgo/web"
	"github.com/memocash/memo/app/auth"
	"github.com/memocash/memo/app/bitcoin/memo"
	"github.com/memocash/memo/app/bitcoin/transaction"
	"github.com/memocash/memo/app/bitcoin/wallet"
	"github.com/memocash/memo/app/config"
	"github.com/memocash/memo/app/db"
	"github.com/memocash/memo/app/mutex"
	"github.com/memocash/memo/app/res"
	"github.com/memocash/memo/app/util"
	"github.com/nfnt/resize"
	"github.com/oliamb/cutter"
	"image/jpeg"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
)

const (
	ResizeLg  = 640
	ResizeMed = 128
	ResizeSm  = 24

	PicPath = "web/public/img/profilepics/"
)

var setPicRoute = web.Route{
	Pattern:    res.UrlMemoSetProfilePic,
	NeedsLogin: true,
	Handler: func(r *web.Response) {
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
		hasSpendableTxOut, err := db.HasSpendable(key.PkHash)
		if err != nil {
			r.Error(jerr.Get("error getting spendable tx out", err), http.StatusInternalServerError)
			return
		}
		if ! hasSpendableTxOut {
			r.SetRedirect(res.UrlNeedFunds)
			return
		}
		r.Render()
	},
}

// Transform https://imgur.com/xSSV7Sg into https://i.imgur.com/xSSV7Sg.jpg and return the string.
func processImgurUrl(url string) (string, error) {

	// Nothing to do.
	if util.ValidateImgurDirectJpg(url) {
		return url, nil
	}

	// Transform to direct link and validate.
	var re = regexp.MustCompile(`(https://([a-z]+\.)?imgur\.com/)([^\s]*)`)
	url = re.ReplaceAllString(url, `https://i.imgur.com/$3.jpg`)

	if util.ValidateImgurDirectJpg(url) {
		return url, nil
	} else {
		return "", jerr.New("invalid imgur link")
	}
}

var setPicSubmitRoute = web.Route{
	Pattern:     res.UrlMemoSetProfilePicSubmit,
	NeedsLogin:  true,
	CsrfProtect: true,
	Handler: func(r *web.Response) {
		url := r.Request.GetFormValue("url")
		url, err := processImgurUrl(url)
		if err != nil {
			r.Error(jerr.Get("invalid profile pic url", err), http.StatusInternalServerError)
			return
		}

		password := r.Request.GetFormValue("password")
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

		privateKey, err := key.GetPrivateKey(password)
		if err != nil {
			r.Error(jerr.Get("error getting private key", err), http.StatusUnauthorized)
			return
		}

		address := key.GetAddress()

		// fetch and save image
		urlMatch, err := regexp.Match(`(^http[s]?://[^\s]*[^.?!,)\s])`, []byte(url))
		if err != nil || urlMatch == false {
			r.Error(jerr.Get("must pass an image url", err), http.StatusUnprocessableEntity)
			return
		}

		response, err := http.Get(url)
		if err != nil {
			r.Error(jerr.Get("couldn't fetch remote image", err), http.StatusInternalServerError)
			return
		}
		defer response.Body.Close()

		FetchProfilePic(url, address.GetAddress().String())

		var fee = int64(memo.MaxTxFee - memo.MaxPostSize + len([]byte(url)))
		var minInput = fee + transaction.DustMinimumOutput

		mutex.Lock(key.PkHash)
		txOuts, err := db.GetSpendableTxOuts(key.PkHash, minInput)
		if err != nil {
			mutex.Unlock(key.PkHash)
			r.Error(jerr.Get("error getting spendable tx out", err), http.StatusInternalServerError)
			return
		}

		var totalValue int64
		for _, txOut := range txOuts {
			totalValue += txOut.Value
		}

		tx, err := transaction.Create(txOuts, privateKey, []transaction.SpendOutput{{
			Type:    transaction.SpendOutputTypeP2PK,
			Address: address,
			Amount:  totalValue - fee,
		}, {
			Type: transaction.SpendOutputTypeMemoSetPic,
			Data: []byte(url),
		}})
		if err != nil {
			mutex.Unlock(key.PkHash)
			r.Error(jerr.Get("error creating tx", err), http.StatusInternalServerError)
			return
		}

		fmt.Println(transaction.GetTxInfo(tx))
		transaction.QueueTx(tx)
		r.Write(tx.TxHash().String())
	},
}

// test localhost:3000/memo/get-profile-pic?address=13MuoY8fLzES35bNsMveiQR7eR93LtxBmy&height=128
var getProfilePicRoute = web.Route{
	Pattern:     res.UrlMemoGetProfilePic,
	NeedsLogin:  false,
	CsrfProtect: false,
	Handler: func(r *web.Response) {
		address := r.Request.GetFormValue("address")
		height := r.Request.GetFormValue("height")

		if !util.ValidateBitcoinLegacyAddress(address) || !util.ValidateProfilePicHeight(height) {
			r.Error(jerr.New("invalid input"), http.StatusInternalServerError)
			return
		}

		profilePicPath := PicPath + address
		img, err := os.Open(profilePicPath + "-" + height + "x" + height + ".jpg")
		if err != nil {
			decodedAddress, addrErr := btcutil.DecodeAddress(address, &wallet.MainNetParamsOld)

			if addrErr != nil {
				r.Error(jerr.New("could not decode address"), http.StatusInternalServerError)
				return
			} else {
				pic, getPicErr := db.GetPicForPkHash(decodedAddress.ScriptAddress())
				if getPicErr != nil {
					r.Error(jerr.New("could not fetch profile pic"), http.StatusInternalServerError)
					return
				}
				// The profile pic exists but not on the file system. Fetch it.
				if pic != nil {
					err = FetchProfilePic(pic.Url, address)
					if err != nil {
						r.Error(jerr.Get("could not save profile pic", err), http.StatusInternalServerError)
						return
					}
				}
			}

			r.Error(jerr.New("could not open os.Open()"), http.StatusInternalServerError)
			return
		}
		defer img.Close()
		r.Writer.Header().Set("Content-Type", "image/jpeg")
		io.Copy(r.Writer, img)
	},
}

func resizeExternally(from string, to string, width uint, height uint) error {
	var args = []string{
		"--size", strconv.FormatUint(uint64(width), 10) + "x" +
			strconv.FormatUint(uint64(height), 10),
		"--output", to,
		"--crop",
		from,
	}
	path, err := exec.LookPath(config.GetFilePaths().VipsThumbnailPath)
	if err != nil {
		return err
	}
	cmd := exec.Command(path, args...)
	return cmd.Run()
}

// Call when a profile pic doesn't exist on the file system.
func FetchProfilePic(url string, address string) error {

	response, err := http.Get(url)
	if err != nil {
		return jerr.Get("couldn't fetch remote image", err)
	}
	defer response.Body.Close()

	if _, err := os.Stat(PicPath); os.IsNotExist(err) {
		err = os.Mkdir(PicPath, 0755)
		if err != nil {
			return jerr.Get("unable to create pic path", err)
		}
	}
	profilePicName := PicPath + address
	file, err := os.Create(profilePicName + ".jpg")
	if err != nil {
		return jerr.Get("couldn't create image file", err)
	}

	_, err = io.Copy(file, response.Body)
	if err != nil {
		return jerr.Get("couldn't save image file", err)
	}
	err = file.Close()
	if err != nil {
		return jerr.Get("error closing file", err)
	}

	// Resize. vipsthumbnail (super fast) integration is off by default.
	if !config.GetFilePaths().UseVipsThumbnail {

		file, err := os.Open(profilePicName + ".jpg")
		if err != nil {
			return jerr.Get("couldn't open fetched profile pic", err)
		}

		// Decode jpeg into image.Image.
		img, err := jpeg.Decode(file)
		if err != nil {
			return jerr.Get("couldn't decode jpg profile pic", err)
		}

		widths := []int{ResizeSm, ResizeMed, ResizeLg}
		for _, width := range widths {

			// Some square crop handling.
			ratio := float32(img.Bounds().Max.X) / float32(img.Bounds().Max.Y)
			ratioY := float32(img.Bounds().Max.Y) / float32(img.Bounds().Max.X)
			if ratioY > ratio {
				ratio = ratioY
			}
			resizeWidth := uint(float32(width) * ratio)

			// Resize to resizeWidth using Lanczos resampling and preserve aspect ratio.
			resizedImg := resize.Resize(resizeWidth, 0, img, resize.Lanczos3)

			croppedImg, err := cutter.Crop(resizedImg, cutter.Config{
				Width:  width,
				Height: width,
				Mode:   cutter.Centered,
			})
			if err != nil {
				return jerr.Get("error cropping image", err)
			}

			out, err := os.Create(profilePicName + "-" + strconv.Itoa(width) + "x" + strconv.Itoa(width) + ".jpg")
			if err != nil {
				return jerr.Get("couldn't create profile pic file", err)
			}

			// Write new image to file.
			err = jpeg.Encode(out, croppedImg, nil)
			if err != nil {
				return jerr.Get("error encoding cropped image", err)
			}
			err = out.Close()
			if err != nil {
				return jerr.Get("error saving cropped image", err)
			}
		}

	} else {
		err = resizeExternally(profilePicName+".jpg", profilePicName+"-"+strconv.Itoa(ResizeSm)+"x"+strconv.Itoa(ResizeSm)+".jpg", ResizeSm, ResizeSm)
		if err != nil {
			return jerr.Get("couldn't resize image file", err)
		}
		err = resizeExternally(profilePicName+".jpg", profilePicName+"-"+strconv.Itoa(ResizeMed)+"x"+strconv.Itoa(ResizeMed)+".jpg", ResizeMed, ResizeMed)
		if err != nil {
			return jerr.Get("couldn't resize image file", err)
		}
		err = resizeExternally(profilePicName+".jpg", profilePicName+"-"+strconv.Itoa(ResizeLg)+"x"+strconv.Itoa(ResizeLg)+".jpg", ResizeLg, ResizeLg)
		if err != nil {
			return jerr.Get("couldn't resize image file", err)
		}
	}
	err = os.Remove(profilePicName + ".jpg")
	if err != nil {
		return jerr.Get("error removing profile pic", err)
	}

	return nil
}
