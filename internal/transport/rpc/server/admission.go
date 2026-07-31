package server

import (
	"sync"
	"time"

	config "github.com/SuzukiHonoka/spaceship/v2/pkg/config/server"
	"golang.org/x/time/rate"
)

type admissionRejection uint8

const (
	admissionAllowed admissionRejection = iota
	admissionRejectedGlobalConcurrency
	admissionRejectedUserConcurrency
	admissionRejectedGlobalRate
	admissionRejectedUserRate
)

type admissionLimits struct {
	maxConcurrent        int
	maxConcurrentPerUser int
	rate                 int
	ratePerUser          int
	burst                int
	burstPerUser         int
}

type userAdmission struct {
	slots chan struct{}
	rate  *rate.Limiter
}

// admission is a non-blocking, process-local concurrency and token-bucket
// boundary. A single mutex serializes the two token reservations so rolling
// back one side cannot lose tokens to a concurrent reservation on the other
// side. Slot release remains lock-free and is safe to call defensively.
type admission struct {
	mu          sync.Mutex
	globalSlots chan struct{}
	globalRate  *rate.Limiter
	users       map[string]*userAdmission
	unknownUser *userAdmission
}

func newAdmission(limits admissionLimits, users config.Users) *admission {
	result := &admission{
		globalSlots: make(chan struct{}, limits.maxConcurrent),
		globalRate: rate.NewLimiter(
			rate.Limit(limits.rate),
			limits.burst,
		),
		users: make(map[string]*userAdmission, len(users)),
	}
	newUser := func() *userAdmission {
		return &userAdmission{
			slots: make(chan struct{}, limits.maxConcurrentPerUser),
			rate: rate.NewLimiter(
				rate.Limit(limits.ratePerUser),
				limits.burstPerUser,
			),
		}
	}
	for _, user := range users {
		if user != nil {
			result.users[user.UUID] = newUser()
		}
	}
	// Direct handler calls do not pass through the authentication interceptor.
	// Keep them bounded under one finite identity. Production requests always use
	// a pre-created authenticated identity above.
	result.unknownUser = newUser()
	return result
}

func (a *admission) acquire(userID string) (func(), admissionRejection) {
	if a == nil {
		return func() {}, admissionAllowed
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	user := a.users[userID]
	if user == nil {
		user = a.unknownUser
	}

	select {
	case a.globalSlots <- struct{}{}:
	default:
		return nil, admissionRejectedGlobalConcurrency
	}
	select {
	case user.slots <- struct{}{}:
	default:
		<-a.globalSlots
		return nil, admissionRejectedUserConcurrency
	}

	now := time.Now()
	userReservation := user.rate.ReserveN(now, 1)
	if !reservationImmediate(userReservation, now) {
		userReservation.CancelAt(now)
		<-user.slots
		<-a.globalSlots
		return nil, admissionRejectedUserRate
	}
	globalReservation := a.globalRate.ReserveN(now, 1)
	if !reservationImmediate(globalReservation, now) {
		globalReservation.CancelAt(now)
		userReservation.CancelAt(now)
		<-user.slots
		<-a.globalSlots
		return nil, admissionRejectedGlobalRate
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			<-user.slots
			<-a.globalSlots
		})
	}, admissionAllowed
}

func reservationImmediate(reservation *rate.Reservation, now time.Time) bool {
	return reservation.OK() && reservation.DelayFrom(now) == 0
}
