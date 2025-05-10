;; vcg_auction.clar - Truthful Multi-Agent Resource Allocation

(define-constant CONTRACT_OWNER (as-contract tx-sender))
(define-constant MIN_BID (u1000000)) ;; 1.0 STX
(define-constant AUCTION_DURATION (u1440)) ;; 24 hours

;; Data Models
(define-data-var auction-active bool false)
(define-data-var auction-end-block uint u0)
(define-data-var item (optional (string-utf8)) none)

(define-map bids principal uint)
(define-map payments principal uint)
(define-map social-welfare uint)

;; Events
(define-event AuctionCreated (item: (string-utf8) end-block: uint))
(define-event BidReceived (bidder: principal amount: uint))
(define-event AuctionSettled (winner: principal payment: uint))

;; Error Codes
(define-err already-bid (err u1001))
(define-err auction-closed (err u1002))
(define-err invalid-bid (err u1003))
(define-err unauthorized (err u1004))

;; Initialize new VCG auction
(define-public (create-auction (item-utf8 (string-utf8)))
    (begin
        (asserts! (is-eq tx-sender CONTRACT_OWNER) (err unauthorized))
        (asserts! (is-none (var-get item)) (err u1005))
        (var-set item (some item-utf8))
        (var-set auction-active true)
        (var-set auction-end-block (+ block-height AUCTION_DURATION))
        (ok (event-emit AuctionCreated item-utf8 (var-get auction-end-block)))
    )
)

;; Submit sealed bid
(define-public (submit-bid (bid-amount uint))
    (let (
        (current-bid (default-to (u0) (map-get? bids tx-sender)))
        (current-block block-height)
    )
        (asserts! (var-get auction-active) (err auction-closed))
        (asserts! (> bid-amount current-bid) (err invalid-bid))
        (asserts! (>= bid-amount MIN_BID) (err invalid-bid))
        (asserts! (<= current-block (var-get auction-end-block)) (err auction-closed))
        
        (map-set bids tx-sender bid-amount)
        (ok (event-emit BidReceived tx-sender bid-amount))
    )
)

;; VCG Core Algorithm
(define-private (calculate-social-cost (winner principal))
    (let (
        ;; Get all bids except winner's
        (others-bids (filter 
            (lambda ((bidder principal) (amount uint)) (not (is-eq bidder winner))) 
            (map-entries bids)
        ))
        ;; Sort descending
        (sorted-bids (sort! (map amount others-bids) >))
    )
        (if (>= (length sorted-bids) 2)
            (nth u1 sorted-bids) ;; Second-highest external bid
            (u0)
        )
    )
)

;; Settle auction and calculate payments
(define-public (settle-auction)
    (let (
        (all-bids (map-entries bids))
        (highest-bid (nth u0 (sort! (map (lambda ((x principal) (y uint)) y) all-bids) >)))
        (winner (if (is-none (element-at (map (lambda ((x principal) (y uint)) x) all-bids) u0)) 
                    none 
                    (some (unwrap! (element-at (map (lambda ((x principal) (y uint)) x) all-bids) u0) tx-sender))
                ))
        )
    )
        (asserts! (is-eq tx-sender CONTRACT_OWNER) (err unauthorized))
        (asserts! (var-get auction-active) (err u1006))
        
        (var-set auction-active false)
        
        (match winner to-print 
            winner-principal => (let (
                    (payment (calculate-social-cost winner-principal))
                )
                (map-set payments winner-principal payment)
                (event-emit AuctionSettled winner-principal payment)
                (ok (list winner-principal payment))
            )
            none => (err u1007)
        )
    )
)

;; Query functions
(define-read-only (get-winning-price)
    (if (var-get auction-active)
        none
        (let ((winner (unwrap! (element-at (map (lambda ((x principal) (y uint)) x) (map-entries bids)) u0) tx-sender)))
            (map-get? payments winner)
        )
    )
)

(define-read-only (get-auction-status)
    { 
        active: (var-get auction-active), 
        end-block: (var-get auction-end-block),
        item: (var-get item),
        min-bid: MIN_BID 
    }
)
