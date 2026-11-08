package service

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/pantalan/pantalan/internal/domain"
)

// AdminService manages the Admin account. Passwords are always generated
// (crypto/rand.Text, 26 base32 characters, 130 bits), never chosen.
type AdminService struct {
	admins AdminRepository
	hasher PasswordHasher
	now    func() time.Time
}

func NewAdminService(admins AdminRepository, hasher PasswordHasher) *AdminService {
	return &AdminService{admins: admins, hasher: hasher, now: time.Now}
}

// Create creates the Admin and returns its generated password. It fails
// with domain.ErrInvalid for a bad username and domain.ErrConflict when an
// Admin already exists.
func (s *AdminService) Create(ctx context.Context, username string) (domain.Admin, string, error) {
	username, err := domain.NormalizeUsername(username)
	if err != nil {
		return domain.Admin{}, "", err
	}
	password, hash, err := s.generate()
	if err != nil {
		return domain.Admin{}, "", err
	}
	now := s.now().UTC()
	admin := domain.Admin{Username: username, PasswordHash: hash, CreatedAt: now, PasswordChangedAt: now}
	if err := s.admins.CreateAdmin(ctx, admin); err != nil {
		return domain.Admin{}, "", fmt.Errorf("create admin: %w", err)
	}
	return admin, password, nil
}

// Get returns the Admin, or domain.ErrNotFound when there is none yet.
func (s *AdminService) Get(ctx context.Context) (domain.Admin, error) {
	return s.admins.GetAdmin(ctx)
}

// ResetPassword gives the Admin a new generated password, signs out every
// session and clears the failure count, all at once, and returns the
// password. It fails with domain.ErrNotFound when there is no Admin.
func (s *AdminService) ResetPassword(ctx context.Context) (string, error) {
	password, hash, err := s.generate()
	if err != nil {
		return "", err
	}
	if err := s.admins.ResetAdminPassword(ctx, hash, s.now().UTC()); err != nil {
		return "", fmt.Errorf("reset admin password: %w", err)
	}
	return password, nil
}

func (s *AdminService) generate() (password, hash string, err error) {
	password = rand.Text()
	hash, err = s.hasher.Hash(password)
	if err != nil {
		return "", "", fmt.Errorf("hash password: %w", err)
	}
	return password, hash, nil
}
