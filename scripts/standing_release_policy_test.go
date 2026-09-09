package main

import (
	"strings"
	"testing"
)

func TestPolicyCheckerAcceptsBoundedStandingRelease(t *testing.T) {
	if problems := checkPolicy(makeRepo(t, readProjectPolicy(t))); len(problems) != 0 {
		t.Fatalf("bounded standing release rejected: %v", problems)
	}
}

func TestPolicyCheckerProtectsStandingReleaseLimits(t *testing.T) {
	policy := readProjectPolicy(t)
	for _, limit := range []string{
		"после полного завершения разработки",
		"Review, диагностика и незавершённая задача не запускают выпуск",
		"local-only, no-push или запрет restart имеют приоритет",
		"только текущий репозиторий Time4Mind/bria",
		"origin/main без force",
		"gui/501/com.time4mind.bria.v2 на текущей машине",
		"Другие клоны, репозитории, hosts и services не получают разрешения автоматически",
		"read-only исполнители не выполняют mutations",
		"показывает точный manifest изменений и артефактов",
		"выполнить полный make check-full",
		"Дождаться успешных обязательных CI для точного итогового SHA в origin/main",
		"изменённую версию снова полностью проверить до установки",
		"После каждого существенного write повторно read-only проверить целевое состояние",
		"пользовательские settings/session/history/journal сохранены",
		"не разрешает изменения secrets, config/state, исходящие messages, destructive операции",
		"Ограничения и запросы разрешений инструментов более высокого приоритета сохраняются",
	} {
		t.Run(limit, func(t *testing.T) {
			if !strings.Contains(policy, limit) {
				t.Fatalf("release policy does not state limit %q", limit)
			}
			mutated := strings.Replace(policy, limit, "ограничение удалено", 1)
			assertErrorContains(t, checkPolicy(makeRepo(t, mutated)), "required clause")
		})
	}
}
