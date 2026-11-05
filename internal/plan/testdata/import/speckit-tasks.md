# Tasks: Discount codes

**Input**: Design documents from `/specs/001-discounts/`

## Format: `[ID] [P?] [Story] Description`

## Phase 1: Setup (Shared Infrastructure)

- [x] T001 Create project structure per implementation plan
- [ ] T002 [P] Add the discount settings in src/billing/settings.py

## Phase 2: Foundational (Blocking Prerequisites)

- [ ] T003 Implement the discount rule in src/billing/discount.py, never letting a total go negative

## Phase 3: User Story 1 - Apply a code at checkout (Priority: P1) 🎯 MVP

- [ ] T004 [P] [US1] Contract test for POST /discounts in tests/api/test_discounts.py
- [ ] T005 [US1] Implement POST /discounts in src/api/discounts.py (depends on T004)

## Phase 4: User Story 2 - Report the codes (Priority: P2)

- [ ] T006 [P] [US2] Show each applied code in the monthly report in src/reports/discounts.py

## Phase 5: Polish & Cross-Cutting Concerns

- [ ] T007 [P] Document every code in docs/discounts.md
