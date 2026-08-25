package diseasecategoriesvc

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/domain/diseasecategories"
	"github.com/bnursik/business_surgery_backend/internal/domain/users"
)

type service struct {
	repo diseasecategories.Repository
}

func New(repo diseasecategories.Repository) diseasecategories.Service {
	return &service{repo: repo}
}

func (s *service) List(ctx context.Context) ([]diseasecategories.Category, error) {
	return s.repo.List(ctx)
}

func (s *service) Get(ctx context.Context, id string) (diseasecategories.Category, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return diseasecategories.Category{}, users.ErrInvalidArgument
	}
	return s.repo.GetByID(ctx, id)
}

func (s *service) Create(ctx context.Context, c diseasecategories.Category) (diseasecategories.Category, error) {
	title := strings.TrimSpace(c.Title)
	if title == "" {
		return diseasecategories.Category{}, users.ErrInvalidArgument
	}

	base := generateCode(title)
	if base == "" {
		base = "category"
	}

	for attempt := 0; attempt < 20; attempt++ {
		code := base
		if attempt > 0 {
			code = fmt.Sprintf("%s-%d", base, attempt+1)
		}

		out, err := s.repo.Create(ctx, diseasecategories.Category{
			Title: title,
			Code:  code,
		})
		if err == nil {
			return out, nil
		}

		if errors.Is(err, users.ErrConflict) {
			continue
		}
		return diseasecategories.Category{}, err
	}

	// fallback: почти гарантированно уникальный код
	code := fmt.Sprintf("%s-%s", base, strconv.FormatInt(time.Now().UnixNano(), 36))
	return s.repo.Create(ctx, diseasecategories.Category{Title: title, Code: code})
}

func (s *service) Update(ctx context.Context, id string, c diseasecategories.Category) (diseasecategories.Category, error) {
	id = strings.TrimSpace(id)
	title := strings.TrimSpace(c.Title)

	if id == "" || title == "" {
		return diseasecategories.Category{}, users.ErrInvalidArgument
	}

	base := generateCode(title)
	if base == "" {
		base = "category"
	}

	for attempt := 0; attempt < 20; attempt++ {
		code := base
		if attempt > 0 {
			code = fmt.Sprintf("%s-%d", base, attempt+1)
		}

		out, err := s.repo.Update(ctx, id, diseasecategories.Category{
			Title: title,
			Code:  code,
		})
		if err == nil {
			return out, nil
		}

		if errors.Is(err, users.ErrConflict) {
			continue
		}
		return diseasecategories.Category{}, err
	}

	code := fmt.Sprintf("%s-%s", base, strconv.FormatInt(time.Now().UnixNano(), 36))
	return s.repo.Update(ctx, id, diseasecategories.Category{Title: title, Code: code})
}

func (s *service) Delete(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return users.ErrInvalidArgument
	}
	return s.repo.Delete(ctx, id)
}

func generateCode(title string) string {
	code := strings.ToLower(strings.TrimSpace(title))
	code = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(code, "-")
	code = strings.Trim(code, "-")
	return code
}
